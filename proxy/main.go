package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/openconfig/gnmi/cache"
	pb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/gnmi/subscribe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Server  ServerConfig
	Mapping MappingConfig
}

// server_config.yaml 用
type ServerConfig struct {
	Port       int    `yaml:"port"`
	SnmpTarget string `yaml:"snmp_target"`
	Community  string `yaml:"community"`
	//RefreshIntervalSec int    `yaml:"refresh_interval_sec"`
	IfNameOID              string `yaml:"if_name_oid"`
	MapRefreshIntervalSec  int    `yaml:"refresh_interval_sec"`
	CacheUpdateIntervalSec int    `yaml:"cache_interval_sec"`
}

// mapping.yaml 用
type MappingConfig struct {
	Paths map[string]PathConf `yaml:"paths"`
}

type PathConf struct {
	OID           string `yaml:"oid"`
	OIDBase       string `yaml:"oid_base"`
	Type          string `yaml:"type"`
	RequiresIndex bool   `yaml:"requires_index"`
}

var (
	config     AppConfig
	ifIndexMap = make(map[string]string)
	mapMutex   sync.RWMutex
	targetName = "local"
)

type Server struct {
	pb.UnimplementedGNMIServer
	cache           *cache.Cache
	subscribeServer *subscribe.Server
}

func (s *Server) Capabilities(ctx context.Context, req *pb.CapabilityRequest) (*pb.CapabilityResponse, error) {
	return &pb.CapabilityResponse{
		SupportedModels: []*pb.ModelData{
			// OpenConfig関係ないし本来使えないのでそのこと明示
			{
				Name:         "gNMI-SNMP-Gateway",
				Organization: "Generic-Gateway",
				Version:      "0.0.1",
			},
		},
		SupportedEncodings: []pb.Encoding{pb.Encoding_JSON_IETF, pb.Encoding_PROTO},
		GNMIVersion:        "0.7.0",
	}, nil
}

func (s *Server) collectUpdates(reqPath *pb.Path) ([]*pb.Update, error) {
	var updates []*pb.Update
	reqPathStr, reqKeys := parsePath(reqPath)

	for definedPath, mapping := range config.Mapping.Paths {
		if !strings.HasPrefix(definedPath, reqPathStr) {
			continue
		}

		// Index不要
		if !mapping.RequiresIndex {
			val, err := fetchSNMP(mapping.OID)
			if err != nil {
				return nil, fmt.Errorf("SNMP failed for %s (OID: %s): %w", definedPath, mapping.OID, err)
			}
			updates = append(updates, &pb.Update{
				Path: stringToPath(definedPath, nil),
				Val:  convertToTypedValue(val, mapping.Type),
			})
			continue
		}

		// Index必要
		mapMutex.RLock()
		currentMap := make(map[string]string)
		for k, v := range ifIndexMap {
			currentMap[k] = v
		}
		mapMutex.RUnlock()

		if reqName, ok := reqKeys["name"]; ok {
			// キー指定あり
			if idx, found := currentMap[reqName]; found {
				targetOID := mapping.OIDBase + "." + idx
				val, err := fetchSNMP(targetOID)
				if err != nil {
					return nil, fmt.Errorf("SNMP failed for %s (OID: %s): %w", definedPath, targetOID, err)
				}
				updates = append(updates, &pb.Update{
					Path: stringToPath(definedPath, map[string]string{"name": reqName}),
					Val:  convertToTypedValue(val, mapping.Type),
				})
			}
		} else {
			// キー指定なし
			for ifName, idx := range currentMap {
				targetOID := mapping.OIDBase + "." + idx
				val, err := fetchSNMP(targetOID)
				if err != nil {
					return nil, fmt.Errorf("SNMP failed for %s[%s]: %w", definedPath, ifName, err)
				}
				updates = append(updates, &pb.Update{
					Path: stringToPath(definedPath, map[string]string{"name": ifName}),
					Val:  convertToTypedValue(val, mapping.Type),
				})
			}
		}
	}
	return updates, nil
}

// --- Get ---(cache不使用)
/*
func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if len(req.Path) == 0 {
		return &pb.GetResponse{}, nil
	}

	updates, err := s.collectUpdates(req.Path[0])
	if err != nil {
		// codes.Unavailableなのかcodes.Internalなのか調べとく
		return nil, status.Errorf(codes.Unavailable, "Backend SNMP Error: %v", err)
	}

	if len(updates) == 0 {
		log.Printf("No data found for path: %v", req.Path[0])
		// NotFound返す
		return nil, status.Errorf(codes.NotFound, "No data found for the requested path. Check mapping.yaml or SNMP device status.")
	}

	return &pb.GetResponse{
		Notification: []*pb.Notification{
			{Timestamp: time.Now().UnixNano(), Update: updates},
		},
	}, nil
}
*/
// --- Subscribe ---
/*
func (s *Server) Subscribe(stream pb.GNMI_SubscribeServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	subList := req.GetSubscribe()
	if subList == nil {
		return fmt.Errorf("subscribe request must contain SubscriptionList")
	}

	mode := subList.Mode
	log.Printf("Subscribe Mode: %v", mode)

	interval := 10 * time.Second
	var targetPaths []*pb.Path

	for _, sub := range subList.Subscription {
		targetPaths = append(targetPaths, sub.Path)
		if sub.SampleInterval > 0 {
			interval = time.Duration(sub.SampleInterval)
		}
	}

	if err := stream.Send(&pb.SubscribeResponse{
		Response: &pb.SubscribeResponse_SyncResponse{SyncResponse: true},
	}); err != nil {
		return err
	}

	if mode != pb.SubscriptionList_STREAM {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sendUpdate := func(t time.Time) error {
		var allUpdates []*pb.Update
		for _, p := range targetPaths {
			updates, err := s.collectUpdates(p)
			if err != nil {
				log.Printf("Subscribe collection failed: %v", err)
				return status.Errorf(codes.Unavailable, "SNMP Error during subscribe: %v", err)
			}
			allUpdates = append(allUpdates, updates...)
		}
		if len(allUpdates) > 0 {
			return stream.Send(&pb.SubscribeResponse{
				Response: &pb.SubscribeResponse_Update{
					Update: &pb.Notification{Timestamp: t.UnixNano(), Update: allUpdates},
				},
			})
		}
		if len(allUpdates) == 0 {
			log.Println("No data collected for subscription. Closing stream.")
			return status.Errorf(codes.NotFound, "No data found for subscribed paths.")
		}
		return nil
	}

	if err := sendUpdate(time.Now()); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			log.Println("Client disconnected")
			return nil
		case t := <-ticker.C:
			if err := sendUpdate(t); err != nil {
				return err
			}
		}
	}
}
*/

type collectorStream struct {
	grpc.ServerStream
	ctx           context.Context
	req           *pb.SubscribeRequest // Cacheに渡すためのリクエスト
	reqSent       bool                 // リクエストをRecvで渡したかどうかのフラグ
	notifications []*pb.Notification   // 収集した通知データ
	err           error
}

func NewCollectorStream(ctx context.Context, getReq *pb.GetRequest) *collectorStream {
	subs := []*pb.Subscription{}
	for _, p := range getReq.Path {
		subs = append(subs, &pb.Subscription{Path: p})
	}

	// 1. Prefix の安全な初期化
	prefix := getReq.Prefix
	if prefix == nil {
		prefix = &pb.Path{}
	}
	// 2. Target が空なら、サーバーで定義している targetName ("device1") を補完する
	// これがないと、Cache内の "device1" のデータとマッチしません
	if prefix.Target == "" {
		prefix.Target = targetName
	}

	return &collectorStream{
		ctx: ctx,
		req: &pb.SubscribeRequest{
			Request: &pb.SubscribeRequest_Subscribe{
				Subscribe: &pb.SubscriptionList{
					Prefix:       prefix, // 補完した prefix を渡す
					Subscription: subs,
					Mode:         pb.SubscriptionList_ONCE,
					Encoding:     getReq.Encoding,
				},
			},
		},
	}
}

func (cs *collectorStream) Context() context.Context {
	if cs.ctx == nil {
		return context.Background()
	}
	return cs.ctx
}

func (cs *collectorStream) Send(resp *pb.SubscribeResponse) error {
	switch v := resp.Response.(type) {
	case *pb.SubscribeResponse_Update:
		// データ更新通知 (Notification) を蓄積
		cs.notifications = append(cs.notifications, v.Update)
	case *pb.SubscribeResponse_SyncResponse:
		// ONCEモードの完了通知。ここでは特に何もしなくてOK
	}
	return nil
}

func (cs *collectorStream) Recv() (*pb.SubscribeRequest, error) {
	if cs.reqSent {
		// 2回目以降は「これ以上リクエストはない」として EOF を返す
		return nil, fmt.Errorf("EOF") // io.EOF 相当のエラーを返すのが通例ですが、grpc的にはerrorで抜けることが多い
	}
	cs.reqSent = true
	return cs.req, nil
}

func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	// 擬似ストリームを作成
	stream := NewCollectorStream(ctx, req)

	// ★修正: subscribeServer に処理を委譲
	// これにより、ワイルドカードやPrefixの処理が正しく行われます
	err := s.subscribeServer.Subscribe(stream)

	// EOFは正常終了
	if err != nil && err.Error() != "EOF" {
		// gRPCのエラーかどうかチェック
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unknown && st.Message() == "EOF" {
			// 無視
		} else {
			log.Printf("Internal subscribe error during Get: %v", err)
			return nil, status.Errorf(codes.Internal, "failed to retrieve data: %v", err)
		}
	}

	if len(stream.notifications) == 0 {
		return nil, status.Errorf(codes.NotFound, "path not found in cache")
	}

	return &pb.GetResponse{
		Notification: stream.notifications,
	}, nil
}

func (s *Server) Subscribe(stream pb.GNMI_SubscribeServer) error {
	// ★修正: subscribeパッケージのサーバーに丸投げ
	return s.subscribeServer.Subscribe(stream)
}

func startSnmpPoller(c *cache.Cache) {
	t := c.GetTarget(targetName)
	pollAndPush(t) // 初回実行

	// ★修正: CacheUpdateIntervalSec を使用
	ticker := time.NewTicker(time.Duration(config.Server.CacheUpdateIntervalSec) * time.Second)
	for range ticker.C {
		pollAndPush(t)
	}
}

func pollAndPush(t *cache.Target) {
	var allUpdates []*pb.Update

	for definedPath, mapping := range config.Mapping.Paths {
		if !mapping.RequiresIndex {
			val, err := fetchSNMP(mapping.OID)
			if err == nil {
				allUpdates = append(allUpdates, &pb.Update{
					Path: stringToPath(definedPath, nil),
					Val:  convertToTypedValue(val, mapping.Type),
				})
			}
			continue
		}

		mapMutex.RLock()
		currentMap := make(map[string]string)
		for k, v := range ifIndexMap {
			currentMap[k] = v
		}
		mapMutex.RUnlock()

		for ifName, idx := range currentMap {
			targetOID := mapping.OIDBase + "." + idx
			val, err := fetchSNMP(targetOID)
			if err == nil {
				allUpdates = append(allUpdates, &pb.Update{
					Path: stringToPath(definedPath, map[string]string{"name": ifName}),
					Val:  convertToTypedValue(val, mapping.Type),
				})
			}
		}
	}

	if len(allUpdates) > 0 {
		notif := &pb.Notification{
			Timestamp: time.Now().UnixNano(),
			Prefix:    &pb.Path{Target: targetName},
			Update:    allUpdates,
		}
		t.GnmiUpdate(notif)
		log.Printf("Poller: Pushed %d updates to cache", len(allUpdates))
	}
}

// Set (未実装)
func (s *Server) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	return &pb.SetResponse{Timestamp: time.Now().UnixNano()}, nil
}

// --- Helpers ---

func parsePath(p *pb.Path) (string, map[string]string) {
	var parts []string
	keys := make(map[string]string)
	for _, elem := range p.Elem {
		parts = append(parts, elem.Name)
		if elem.Key != nil {
			for k, v := range elem.Key {
				keys[k] = v
			}
		}
	}
	return "/" + strings.Join(parts, "/"), keys
}

func stringToPath(pathStr string, keys map[string]string) *pb.Path {
	parts := strings.Split(strings.Trim(pathStr, "/"), "/")
	var elems []*pb.PathElem
	for _, part := range parts {
		pe := &pb.PathElem{Name: part}
		if part == "interface" && keys != nil {
			pe.Key = keys
		}
		elems = append(elems, pe)
	}
	return &pb.Path{Elem: elems}
}

// config.Server を参照するように変更
func fetchSNMP(oid string) (interface{}, error) {
	client := &gosnmp.GoSNMP{
		Target:    config.Server.SnmpTarget,
		Port:      161,
		Community: config.Server.Community,
		Version:   gosnmp.Version2c,
		Timeout:   time.Duration(1) * time.Second,
		Retries:   0,
	}
	if err := client.Connect(); err != nil {
		return nil, err
	}
	defer client.Conn.Close()

	result, err := client.Get([]string{oid})
	if err != nil {
		return nil, err
	}
	if len(result.Variables) > 0 {
		return result.Variables[0].Value, nil
	}
	return nil, fmt.Errorf("no val")
}

func convertToTypedValue(val interface{}, expectedType string) *pb.TypedValue {
	switch v := val.(type) {
	case []byte:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: string(v)}}
	case string:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: v}}
	case int:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: int64(v)}}
	case int64:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: v}}
	case uint:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: uint64(v)}}
	case uint64:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: v}}
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: fmt.Sprintf("%v", v)}}
	}
}

// config.Server を参照するように変更
func updateInterfaceMap() {
	client := &gosnmp.GoSNMP{
		Target:    config.Server.SnmpTarget,
		Port:      161,
		Community: config.Server.Community,
		Version:   gosnmp.Version2c,
		Timeout:   time.Duration(2) * time.Second,
	}
	if err := client.Connect(); err != nil {
		log.Printf("Map refresh failed: %v", err)
		return
	}
	defer client.Conn.Close()

	newMap := make(map[string]string)
	client.BulkWalk(config.Server.IfNameOID, func(pdu gosnmp.SnmpPDU) error {
		oidParts := strings.Split(pdu.Name, ".")
		index := oidParts[len(oidParts)-1]
		var name string
		switch pdu.Type {
		case gosnmp.OctetString:
			name = string(pdu.Value.([]byte))
		default:
			name = fmt.Sprintf("%v", pdu.Value)
		}
		name = strings.Trim(name, "\x00")
		if name != "" {
			newMap[name] = index
		}
		return nil
	})

	mapMutex.Lock()
	ifIndexMap = newMap
	mapMutex.Unlock()
	log.Printf("Interface map refreshed. Count: %d", len(newMap))
}

func backgroundRefresher() {
	updateInterfaceMap() // 初回実行
	ticker := time.NewTicker(time.Duration(config.Server.MapRefreshIntervalSec) * time.Second)
	for range ticker.C {
		updateInterfaceMap()
	}
}

func main() {
	// server_config.yaml読み込み
	serverData, err := ioutil.ReadFile("server_config.yaml")
	if err != nil {
		log.Fatalf("Failed to read server_config.yaml: %v", err)
	}
	if err := yaml.Unmarshal(serverData, &config.Server); err != nil {
		log.Fatalf("Failed to parse server_config.yaml: %v", err)
	}
	if config.Server.MapRefreshIntervalSec <= 0 {
		config.Server.MapRefreshIntervalSec = 300
		log.Println("Config: refresh_interval_sec (Map) missing. Using default 300s.")
	}

	if config.Server.CacheUpdateIntervalSec <= 0 {
		config.Server.CacheUpdateIntervalSec = 10
		log.Println("Config: cache_interval_sec (Metrics) missing. Using default 10s.")
	}

	log.Printf("Intervals -> Map Refresh: %ds, Cache Update: %ds",
		config.Server.MapRefreshIntervalSec, config.Server.CacheUpdateIntervalSec)
	// mapping.yaml読み込み
	mappingData, err := ioutil.ReadFile("mapping.yaml")
	if err != nil {
		log.Fatalf("Failed to read mapping.yaml: %v", err)
	}
	if err := yaml.Unmarshal(mappingData, &config.Mapping); err != nil {
		log.Fatalf("Failed to parse mapping.yaml: %v", err)
	}

	c := cache.New(nil)
	c.Add(targetName)
	subscribeSrv, err := subscribe.NewServer(c)
	if err != nil {
		log.Fatalf("Failed to create subscribe server: %v", err)
	}

	go backgroundRefresher()
	go startSnmpPoller(c)

	addr := fmt.Sprintf(":%d", config.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	srv := &Server{
		cache:           c,
		subscribeServer: subscribeSrv,
	}
	pb.RegisterGNMIServer(s, srv)
	reflection.Register(s)

	log.Printf("gNMI Server listening on %s", addr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
