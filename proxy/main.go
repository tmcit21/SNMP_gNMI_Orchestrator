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
	Port                   int    `yaml:"port"`
	SnmpTarget             string `yaml:"snmp_target"`
	Community              string `yaml:"community"`
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

// --- グローバル変数 ---
// 重複を削除し、ifIndexMap と mapMutex のみに統一しました
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

// --- Collector Stream Helpers ---

type collectorStream struct {
	grpc.ServerStream
	ctx           context.Context
	req           *pb.SubscribeRequest
	reqSent       bool
	notifications []*pb.Notification
}

type prefixInjectingStream struct {
	pb.GNMI_SubscribeServer
	targetName string
}

func NewCollectorStream(ctx context.Context, getReq *pb.GetRequest) *collectorStream {
	subs := []*pb.Subscription{}
	for _, p := range getReq.Path {
		subs = append(subs, &pb.Subscription{Path: p})
	}

	prefix := getReq.Prefix
	if prefix == nil {
		prefix = &pb.Path{}
	}
	if prefix.Target == "" {
		prefix.Target = targetName
	}

	return &collectorStream{
		ctx: ctx,
		req: &pb.SubscribeRequest{
			Request: &pb.SubscribeRequest_Subscribe{
				Subscribe: &pb.SubscriptionList{
					Prefix:       prefix,
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
		cs.notifications = append(cs.notifications, v.Update)
	case *pb.SubscribeResponse_SyncResponse:
		// Do nothing
	}
	return nil
}

func (cs *collectorStream) Recv() (*pb.SubscribeRequest, error) {
	if cs.reqSent {
		return nil, fmt.Errorf("EOF")
	}
	cs.reqSent = true
	return cs.req, nil
}

func (s *prefixInjectingStream) Recv() (*pb.SubscribeRequest, error) {
	req, err := s.GNMI_SubscribeServer.Recv()
	if err != nil {
		return nil, err
	}
	if sub := req.GetSubscribe(); sub != nil {
		if sub.Prefix == nil {
			sub.Prefix = &pb.Path{}
		}
		if sub.Prefix.Target == "" {
			sub.Prefix.Target = s.targetName
		}
	}
	return req, nil
}

// --- gNMI Handlers ---

func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	// 1. まずキャッシュから取得を試みる
	notifications, err := s.fetchFromCache(ctx, req)
	if err != nil {
		return nil, err
	}

	// 2. キャッシュにデータがあればそれを返す (高速応答)
	if len(notifications) > 0 {
		return &pb.GetResponse{Notification: notifications}, nil
	}

	// 3. データがない場合: "オンデマンド"でSNMPを取りに行く (Fallback)
	// log.Printf("Cache miss for path: %v. Fetching from SNMP...", req.Path)

	// 要求されたパスに関連するOIDを全部取得してキャッシュに突っ込む
	if err := s.syncSnmpToCache(req.Path); err != nil {
		log.Printf("Failed to sync SNMP: %v", err)
		// SNMP失敗しても、キャッシュ取得のエラーではないので、
		// NotFoundを返すためにそのまま進む
	}

	// 4. キャッシュが潤ったはずなので、もう一度キャッシュから取得
	notifications, err = s.fetchFromCache(ctx, req)
	if err != nil {
		return nil, err
	}

	if len(notifications) == 0 {
		return nil, status.Errorf(codes.NotFound, "path not found in cache (even after SNMP fetch)")
	}

	return &pb.GetResponse{Notification: notifications}, nil
}

// 共通処理: キャッシュからの読み出しロジック
func (s *Server) fetchFromCache(ctx context.Context, req *pb.GetRequest) ([]*pb.Notification, error) {
	stream := NewCollectorStream(ctx, req)
	err := s.subscribeServer.Subscribe(stream)
	if err != nil && err.Error() != "EOF" {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unknown && st.Message() == "EOF" {
			// ignore
		} else {
			return nil, status.Errorf(codes.Internal, "failed to retrieve data: %v", err)
		}
	}
	return stream.notifications, nil
}

func (s *Server) Subscribe(stream pb.GNMI_SubscribeServer) error {
	wrappedStream := &prefixInjectingStream{
		GNMI_SubscribeServer: stream,
		targetName:           targetName,
	}
	return s.subscribeServer.Subscribe(wrappedStream)
}

func (s *Server) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	return &pb.SetResponse{Timestamp: time.Now().UnixNano()}, nil
}

// --- Background Tasks ---

func startSnmpPoller(c *cache.Cache) {
	t := c.GetTarget(targetName)
	pollAndPush(t) // 初回実行

	ticker := time.NewTicker(time.Duration(config.Server.CacheUpdateIntervalSec) * time.Second)
	for range ticker.C {
		pollAndPush(t)
	}
}

func pollAndPush(t *cache.Target) {
	// ★修正: WalkAllを使うため、mainで初期化された gosnmp.Default を使用します
	targetOID := ".1.3.6.1.2.1.2.2.1.10" // ifInOctets

	result, err := gosnmp.Default.WalkAll(targetOID)
	if err != nil {
		log.Printf("SNMP Walk Error in Poller: %v", err)
		return
	}

	var updates []*pb.Update
	ts := time.Now().UnixNano()

	mapMutex.RLock()
	defer mapMutex.RUnlock()

	for _, pdu := range result {
		elems := strings.Split(pdu.Name, ".")
		index := elems[len(elems)-1]

		// ★修正: ifIndexMap を使用
		name, ok := ifIndexMap[index]
		if !ok {
			continue
		}

		val, err := snmpValueToGnmiValue(pdu)
		if err != nil {
			continue
		}

		// Key ("name"=name) を含んだパスを作成
		path := &pb.Path{
			Elem: []*pb.PathElem{
				{Name: "interfaces"},
				{Name: "interface", Key: map[string]string{"name": name}},
				{Name: "state"},
				{Name: "counters"},
				{Name: "in-octets"},
			},
		}

		updates = append(updates, &pb.Update{Path: path, Val: val})
	}

	if len(updates) > 0 {
		notif := &pb.Notification{
			Timestamp: ts,
			Prefix:    &pb.Path{Target: targetName},
			Update:    updates,
		}
		if err := t.GnmiUpdate(notif); err != nil {
			log.Printf("Cache Update Failed: %v", err)
		} else {
			// log.Printf("Pushed %d metrics to cache.", len(updates))
		}
	}
}

func updateInterfaceMap() {
	oid := config.Server.IfNameOID
	if oid == "" {
		oid = ".1.3.6.1.2.1.31.1.1.1.1" // デフォルト
	}

	result, err := gosnmp.Default.WalkAll(oid)
	if err != nil {
		log.Printf("Interface Map Walk Error: %v", err)
		return
	}

	mapMutex.Lock()
	defer mapMutex.Unlock()

	// マップを一度クリアするか、上書きするかは要件次第ですが、ここでは上書き更新
	for _, pdu := range result {
		elems := strings.Split(pdu.Name, ".")
		index := elems[len(elems)-1]

		// SNMPの結果はバイト列が多いためStringキャスト
		var name string
		switch v := pdu.Value.(type) {
		case []byte:
			name = string(v)
		case string:
			name = v
		default:
			continue
		}

		// ★修正: ifIndexMap を使用
		ifIndexMap[index] = name
	}

	log.Printf("Updated Interface Map. Total entries: %d", len(ifIndexMap))
}

func backgroundRefresher() {
	updateInterfaceMap() // 初回実行
	ticker := time.NewTicker(time.Duration(config.Server.MapRefreshIntervalSec) * time.Second)
	for range ticker.C {
		updateInterfaceMap()
	}
}

// --- Helpers ---

// ★追加: 欠落していた関数
func snmpValueToGnmiValue(pdu gosnmp.SnmpPDU) (*pb.TypedValue, error) {
	switch val := pdu.Value.(type) {
	case int:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: int64(val)}}, nil
	case int8:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: int64(val)}}, nil
	case int16:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: int64(val)}}, nil
	case int32:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: int64(val)}}, nil
	case int64:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: val}}, nil
	case uint:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: uint64(val)}}, nil
	case uint8:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: uint64(val)}}, nil
	case uint16:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: uint64(val)}}, nil
	case uint32:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: uint64(val)}}, nil
	case uint64:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: val}}, nil
	case []byte:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: string(val)}}, nil
	case string:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: val}}, nil
	default:
		// Counter64などはBigIntになることがあるためgosnmpのヘルパーを利用
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: gosnmp.ToBigInt(pdu.Value).Uint64()}}, nil
	}
}

// Config参照用(Mappingロジックで使用)
func fetchSNMP(oid string) (interface{}, error) {
	// ★修正: 毎回Connectするのではなく、既存のDefaultを使いたいところですが、
	// 既存コードとの互換性のため残します。ただしConfigから生成します。
	client := &gosnmp.GoSNMP{
		Target:    config.Server.SnmpTarget,
		Port:      161,
		Community: config.Server.Community,
		Version:   gosnmp.Version2c,
		Timeout:   time.Duration(2) * time.Second,
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

// Mappingロジックで使用
func convertToTypedValue(val interface{}, expectedType string) *pb.TypedValue {
	switch v := val.(type) {
	case []byte:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: string(v)}}
	case string:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: v}}
	case int, int64:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: gosnmp.ToBigInt(v).Int64()}}
	case uint, uint64:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: gosnmp.ToBigInt(v).Uint64()}}
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: fmt.Sprintf("%v", v)}}
	}
}

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

// 要求されたパスにマッチするマッピング定義を探し、SNMP取得してキャッシュを更新する
func (s *Server) syncSnmpToCache(reqPaths []*pb.Path) error {
	t := s.cache.GetTarget(targetName)
	ts := time.Now().UnixNano()
	var updates []*pb.Update

	for _, reqPath := range reqPaths {
		reqPathStr, reqKeys := parsePath(reqPath)

		// config.Mapping.Paths を走査して、要求パス前方一致するものを探す
		for definedPath, mapping := range config.Mapping.Paths {
			// 例: request="/system", defined="/system/state/hostname" -> マッチする
			if !strings.HasPrefix(definedPath, reqPathStr) {
				continue
			}

			// A. Index不要な単純なOID (例: hostname, uptime)
			if !mapping.RequiresIndex {
				val, err := fetchSNMP(mapping.OID) // 単発Get
				if err != nil {
					log.Printf("SNMP fetch failed for %s: %v", definedPath, err)
					continue
				}
				updates = append(updates, &pb.Update{
					Path: stringToPath(definedPath, nil),
					Val:  convertToTypedValue(val, mapping.Type),
				})
				continue
			}

			// B. Indexが必要な場合 (Interfaceなど)
			// マップからIndex解決を試みる
			mapMutex.RLock()
			currentMap := make(map[string]string)
			for k, v := range ifIndexMap {
				currentMap[k] = v
			}
			mapMutex.RUnlock()

			// キー指定があるか？ (interface[name=lo])
			if reqName, ok := reqKeys["name"]; ok {
				// 特定のIFだけ取得
				for idx, name := range currentMap {
					if name == reqName {
						targetOID := mapping.OIDBase + "." + idx
						val, err := fetchSNMP(targetOID)
						if err == nil {
							updates = append(updates, &pb.Update{
								Path: stringToPath(definedPath, map[string]string{"name": name}),
								Val:  convertToTypedValue(val, mapping.Type),
							})
						}
					}
				}
			} else {
				// キー指定なし (interface全体) -> 全IF取得
				// ※パフォーマンス注意: ここでWalkAllせずループでGetしてますが、
				//  mapping.yamlのRequiresIndex構造上、OIDBase+IndexでGetする形になります
				for idx, name := range currentMap {
					targetOID := mapping.OIDBase + "." + idx
					val, err := fetchSNMP(targetOID)
					if err == nil {
						updates = append(updates, &pb.Update{
							Path: stringToPath(definedPath, map[string]string{"name": name}),
							Val:  convertToTypedValue(val, mapping.Type),
						})
					}
				}
			}
		}
	}

	if len(updates) > 0 {
		notif := &pb.Notification{
			Timestamp: ts,
			Prefix:    &pb.Path{Target: targetName},
			Update:    updates,
		}
		// キャッシュに書き込む！
		return t.GnmiUpdate(notif)
	}
	return nil
}

func main() {
	// 1. Config読み込み
	serverData, err := ioutil.ReadFile("server_config.yaml")
	if err != nil {
		log.Fatalf("Failed to read server_config.yaml: %v", err)
	}
	if err := yaml.Unmarshal(serverData, &config.Server); err != nil {
		log.Fatalf("Failed to parse server_config.yaml: %v", err)
	}

	// デフォルト値補完
	if config.Server.MapRefreshIntervalSec <= 0 {
		config.Server.MapRefreshIntervalSec = 300
	}
	if config.Server.CacheUpdateIntervalSec <= 0 {
		config.Server.CacheUpdateIntervalSec = 10
	}

	mappingData, err := ioutil.ReadFile("mapping.yaml")
	if err != nil {
		log.Fatalf("Failed to read mapping.yaml: %v", err)
	}
	if err := yaml.Unmarshal(mappingData, &config.Mapping); err != nil {
		log.Fatalf("Failed to parse mapping.yaml: %v", err)
	}

	// 2. ★重要: gosnmp.Default のグローバル設定 (WalkAllなどで使われる)
	gosnmp.Default.Target = config.Server.SnmpTarget
	gosnmp.Default.Community = config.Server.Community
	gosnmp.Default.Version = gosnmp.Version2c
	gosnmp.Default.Timeout = time.Duration(2) * time.Second
	if err := gosnmp.Default.Connect(); err != nil {
		log.Fatalf("Failed to connect to SNMP Agent: %v", err)
	}
	defer gosnmp.Default.Conn.Close()

	log.Printf("SNMP Connected to %s", config.Server.SnmpTarget)

	// 3. gNMI Cache / Server初期化
	c := cache.New(nil)
	c.Add(targetName)
	subscribeSrv, err := subscribe.NewServer(c)
	if err != nil {
		log.Fatalf("Failed to create subscribe server: %v", err)
	}

	// 4. ポーリング開始
	// マッピング情報の初回作成
	updateInterfaceMap()

	// 定期タスク開始
	go backgroundRefresher()
	go startSnmpPoller(c)

	// 5. gRPC Server起動
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
