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
	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"gopkg.in/yaml.v3"
)

// --- 設定構造体の定義 ---

// 全体の設定を保持する親構造体
type AppConfig struct {
	Server  ServerConfig
	Mapping MappingConfig
}

// server_config.yaml 用
type ServerConfig struct {
	Port               int    `yaml:"port"`
	SnmpTarget         string `yaml:"snmp_target"`
	Community          string `yaml:"community"`
	RefreshIntervalSec int    `yaml:"refresh_interval_sec"`
	IfNameOID          string `yaml:"if_name_oid"`
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
var (
	config     AppConfig
	ifIndexMap = make(map[string]string)
	mapMutex   sync.RWMutex
)

// --- Server 定義 ---
type Server struct {
	pb.UnimplementedGNMIServer
}

func (s *Server) Capabilities(ctx context.Context, req *pb.CapabilityRequest) (*pb.CapabilityResponse, error) {
	return &pb.CapabilityResponse{
		SupportedModels:    []*pb.ModelData{},
		SupportedEncodings: []pb.Encoding{pb.Encoding_JSON_IETF},
		GNMIVersion:        "0.7.0",
	}, nil
}

// --- 共通データ収集ロジック ---
func (s *Server) collectUpdates(reqPath *pb.Path) []*pb.Update {
	var updates []*pb.Update
	reqPathStr, reqKeys := parsePath(reqPath)

	// config.Mapping.Paths を参照するように変更
	for definedPath, mapping := range config.Mapping.Paths {
		if !strings.HasPrefix(definedPath, reqPathStr) {
			continue
		}

		// A. Index不要
		if !mapping.RequiresIndex {
			val, err := fetchSNMP(mapping.OID)
			if err == nil {
				updates = append(updates, &pb.Update{
					Path: stringToPath(definedPath, nil),
					Val:  convertToTypedValue(val, mapping.Type),
				})
			}
			continue
		}

		// B. Index必要
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
				if err == nil {
					updates = append(updates, &pb.Update{
						Path: stringToPath(definedPath, map[string]string{"name": reqName}),
						Val:  convertToTypedValue(val, mapping.Type),
					})
				}
			}
		} else {
			// キー指定なし (全展開)
			for ifName, idx := range currentMap {
				targetOID := mapping.OIDBase + "." + idx
				val, err := fetchSNMP(targetOID)
				if err == nil {
					updates = append(updates, &pb.Update{
						Path: stringToPath(definedPath, map[string]string{"name": ifName}),
						Val:  convertToTypedValue(val, mapping.Type),
					})
				}
			}
		}
	}
	return updates
}

// --- Get ---
func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if len(req.Path) == 0 {
		return &pb.GetResponse{}, nil
	}
	updates := s.collectUpdates(req.Path[0])
	return &pb.GetResponse{
		Notification: []*pb.Notification{
			{Timestamp: time.Now().UnixNano(), Update: updates},
		},
	}, nil
}

// --- Subscribe ---
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
			updates := s.collectUpdates(p)
			allUpdates = append(allUpdates, updates...)
		}
		if len(allUpdates) > 0 {
			return stream.Send(&pb.SubscribeResponse{
				Response: &pb.SubscribeResponse_Update{
					Update: &pb.Notification{Timestamp: t.UnixNano(), Update: allUpdates},
				},
			})
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
	updateInterfaceMap()
	ticker := time.NewTicker(time.Duration(config.Server.RefreshIntervalSec) * time.Second)
	for range ticker.C {
		updateInterfaceMap()
	}
}

// --- Main ---
func main() {
	// 1. サーバー設定 (server_config.yaml) の読み込み
	serverData, err := ioutil.ReadFile("server_config.yaml")
	if err != nil {
		log.Fatalf("Failed to read server_config.yaml: %v", err)
	}
	if err := yaml.Unmarshal(serverData, &config.Server); err != nil {
		log.Fatalf("Failed to parse server_config.yaml: %v", err)
	}

	// 2. マッピング設定 (mapping.yaml) の読み込み
	mappingData, err := ioutil.ReadFile("mapping.yaml")
	if err != nil {
		log.Fatalf("Failed to read mapping.yaml: %v", err)
	}
	if err := yaml.Unmarshal(mappingData, &config.Mapping); err != nil {
		log.Fatalf("Failed to parse mapping.yaml: %v", err)
	}

	log.Printf("Loaded Config. Port: %d, Target: %s", config.Server.Port, config.Server.SnmpTarget)

	// 自動更新開始
	go backgroundRefresher()

	// 3. 設定ファイルで指定されたポートでリッスン
	addr := fmt.Sprintf(":%d", config.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGNMIServer(s, &Server{})
	reflection.Register(s)

	log.Printf("gNMI Server listening on %s", addr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
