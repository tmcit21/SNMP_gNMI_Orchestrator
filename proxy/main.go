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

// --- 設定構造体 ---

type AppConfig struct {
	Server  ServerConfig
	Mapping MappingConfig
}

type ServerConfig struct {
	Port                   int    `yaml:"port"`
	SnmpTarget             string `yaml:"snmp_target"`
	Community              string `yaml:"community"`
	IfNameOID              string `yaml:"if_name_oid"`
	MapRefreshIntervalSec  int    `yaml:"refresh_interval_sec"`
	CacheUpdateIntervalSec int    `yaml:"cache_interval_sec"`
}

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
	ifIndexMap = make(map[string]string) // Key: Index(1,2...), Val: Name(lo, eth0...)
	mapMutex   sync.RWMutex
)

// --- 簡易キャッシュ (TTL付き) ---
// OpenConfigのキャッシュは複雑すぎるため、研究用にシンプルなものを使用

type CacheEntry struct {
	Value     *pb.TypedValue
	Timestamp int64 // UnixNano
}

type SimpleCache struct {
	sync.RWMutex
	data map[string]CacheEntry
}

func NewSimpleCache() *SimpleCache {
	return &SimpleCache{
		data: make(map[string]CacheEntry),
	}
}

// --- gRPC Server ---

type Server struct {
	pb.UnimplementedGNMIServer
	simpleCache *SimpleCache
}

func NewServer() *Server {
	return &Server{
		simpleCache: NewSimpleCache(),
	}
}

// --- Subscribeの実装 (自前制御版) ---
// クライアントからのリクエストを受け、自前のループで定期的にデータを送ります。
func (s *Server) Subscribe(stream pb.GNMI_SubscribeServer) error {
	// 1. リクエスト受信
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	subscribeReq := req.GetSubscribe()
	if subscribeReq == nil {
		return fmt.Errorf("request must contain subscribe definition")
	}

	// 2. インターバル決定 (最小値採用)
	interval := 10 * time.Second
	for _, sub := range subscribeReq.Subscription {
		if sub.SampleInterval > 0 {
			requested := time.Duration(sub.SampleInterval) * time.Nanosecond
			if requested < interval {
				interval = requested
			}
		}
	}
	// 安全策: 1秒未満は1秒にする
	if interval < 1*time.Second {
		interval = 1 * time.Second
	}

	// 3. 取得対象パスのリスト化
	var targets []*pb.Path
	for _, sub := range subscribeReq.Subscription {
		targets = append(targets, sub.Path)
	}

	log.Printf("Client Subscribed: Interval=%v, Paths=%d", interval, len(targets))

	// 4. SyncResponse 送信 (これがないとクライアントが待機完了にならない)
	stream.Send(&pb.SubscribeResponse{
		Response: &pb.SubscribeResponse_SyncResponse{
			SyncResponse: true,
		},
	})

	// 5. 定期実行ループ
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 初回実行
	if err := s.pushUpdate(stream, subscribeReq.Prefix, targets); err != nil {
		log.Printf("Initial push failed: %v", err)
	}

	for {
		select {
		case <-stream.Context().Done():
			log.Println("Client disconnected")
			return nil
		case <-ticker.C:
			// 定期実行
			if err := s.pushUpdate(stream, subscribeReq.Prefix, targets); err != nil {
				log.Printf("Push failed: %v", err)
				return err
			}
		}
	}
}

// データを取得してクライアントに送信する処理
func (s *Server) pushUpdate(stream pb.GNMI_SubscribeServer, prefix *pb.Path, targets []*pb.Path) error {
	// SNMP (またはキャッシュ) からデータを取得
	updates, err := s.fetchData(targets)
	if err != nil {
		return err
	}

	if len(updates) == 0 {
		return nil
	}

	// レスポンス作成
	timestamp := time.Now().UnixNano()
	resp := &pb.SubscribeResponse{
		Response: &pb.SubscribeResponse_Update{
			Update: &pb.Notification{
				Timestamp: timestamp,
				Prefix:    prefix,
				Update:    updates,
			},
		},
	}

	// 送信 (値が変わっていなくても強制的に送る)
	return stream.Send(resp)
}

// --- データ取得ロジック (キャッシュ + SNMP) ---

func (s *Server) fetchData(reqPaths []*pb.Path) ([]*pb.Update, error) {
	var updates []*pb.Update

	// 設定からTTLを取得 (デフォルト2秒)
	ttlSec := config.Server.CacheUpdateIntervalSec
	if ttlSec <= 0 {
		ttlSec = 2
	}
	ttl := time.Duration(ttlSec) * time.Second

	for _, path := range reqPaths {
		pathKey := fmt.Sprintf("%v", path) // キャッシュキー

		// 1. キャッシュチェック
		s.simpleCache.RLock()
		entry, hit := s.simpleCache.data[pathKey]
		s.simpleCache.RUnlock()

		now := time.Now()

		// ヒットかつTTL内ならキャッシュを使う
		if hit && now.Sub(time.Unix(0, entry.Timestamp)) < ttl {
			updates = append(updates, &pb.Update{Path: path, Val: entry.Value})
			continue
		}

		// 2. キャッシュミス or 期限切れ -> SNMPへ
		val, err := s.fetchSNMPForPath(path)
		if err != nil {
			log.Printf("SNMP Fetch Error: %v", err)
			continue
		}

		// 3. キャッシュ更新
		s.simpleCache.Lock()
		s.simpleCache.data[pathKey] = CacheEntry{
			Value:     val,
			Timestamp: now.UnixNano(),
		}
		s.simpleCache.Unlock()

		updates = append(updates, &pb.Update{Path: path, Val: val})
	}

	return updates, nil
}

// 1つのパスに対して実際にSNMPを叩きに行く処理
func (s *Server) fetchSNMPForPath(path *pb.Path) (*pb.TypedValue, error) {
	pathStr, keys := parsePath(path)

	// マッピング検索
	for definedPath, mapping := range config.Mapping.Paths {
		if !strings.HasPrefix(definedPath, pathStr) {
			continue
		}

		// A. Index不要 (単純OID)
		if !mapping.RequiresIndex {
			raw, err := fetchSNMPRaw(mapping.OID)
			if err != nil {
				return nil, err
			}
			return convertToTypedValue(raw, mapping.Type), nil
		}

		// B. Index必要 (Interface等)
		if mapping.RequiresIndex {
			reqName, ok := keys["name"]
			if !ok {
				return nil, fmt.Errorf("name key required for this path")
			}

			// マップからIndexを探す
			mapMutex.RLock()
			var targetIndex string
			for idx, name := range ifIndexMap {
				if name == reqName {
					targetIndex = idx
					break
				}
			}
			mapMutex.RUnlock()

			if targetIndex == "" {
				return nil, fmt.Errorf("interface %s not found in map", reqName)
			}

			targetOID := mapping.OIDBase + "." + targetIndex
			raw, err := fetchSNMPRaw(targetOID)
			if err != nil {
				return nil, err
			}
			return convertToTypedValue(raw, mapping.Type), nil
		}
	}

	return nil, fmt.Errorf("no mapping found for path")
}

// 単発SNMP取得 (スレッドセーフにするため毎回クライアント作成推奨、またはプール)
// 研究用なので簡易に毎回接続します
func fetchSNMPRaw(oid string) (interface{}, error) {
	client := &gosnmp.GoSNMP{
		Target:    config.Server.SnmpTarget,
		Port:      161,
		Community: config.Server.Community,
		Version:   gosnmp.Version2c,
		Timeout:   2 * time.Second,
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
	return nil, fmt.Errorf("no value returned")
}

// --- その他の必須メソッド (Get, Set, Capabilities) ---

func (s *Server) Capabilities(ctx context.Context, req *pb.CapabilityRequest) (*pb.CapabilityResponse, error) {
	return &pb.CapabilityResponse{
		GNMIVersion: "0.7.0",
		SupportedModels: []*pb.ModelData{
			{Name: "Research-Gateway", Version: "1.0"},
		},
	}, nil
}

func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	// Getリクエストも Subscribe のロジック(fetchData)を再利用
	updates, err := s.fetchData(req.Path)
	if err != nil {
		return nil, err
	}
	return &pb.GetResponse{
		Notification: []*pb.Notification{
			{
				Timestamp: time.Now().UnixNano(),
				Update:    updates,
			},
		},
	}, nil
}

func (s *Server) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	return &pb.SetResponse{Timestamp: time.Now().UnixNano()}, nil
}

// --- Interface Map 更新 (バックグラウンド) ---

func updateInterfaceMap() {
	oid := config.Server.IfNameOID
	if oid == "" {
		oid = ".1.3.6.1.2.1.31.1.1.1.1"
	}

	// マップ更新時はグローバルのDefaultクライアントを利用
	// (Connect済み前提)
	result, err := gosnmp.Default.WalkAll(oid)
	if err != nil {
		log.Printf("Interface Map Walk Error: %v", err)
		return
	}

	mapMutex.Lock()
	defer mapMutex.Unlock()

	// ifIndexMap = make(map[string]string) // クリアしたい場合
	count := 0
	for _, pdu := range result {
		elems := strings.Split(pdu.Name, ".")
		index := elems[len(elems)-1]

		var name string
		switch v := pdu.Value.(type) {
		case []byte:
			name = string(v)
		case string:
			name = v
		default:
			continue
		}
		ifIndexMap[index] = name
		count++
	}
	log.Printf("Updated Interface Map. Total: %d", count)
}

func backgroundRefresher() {
	ticker := time.NewTicker(time.Duration(config.Server.MapRefreshIntervalSec) * time.Second)
	for range ticker.C {
		updateInterfaceMap()
	}
}

// --- ヘルパー関数 ---

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

func convertToTypedValue(val interface{}, expectedType string) *pb.TypedValue {
	switch v := val.(type) {
	case int, int64:
		return &pb.TypedValue{Value: &pb.TypedValue_IntVal{IntVal: gosnmp.ToBigInt(v).Int64()}}
	case uint, uint64:
		return &pb.TypedValue{Value: &pb.TypedValue_UintVal{UintVal: gosnmp.ToBigInt(v).Uint64()}}
	case []byte:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: string(v)}}
	case string:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: v}}
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: fmt.Sprintf("%v", v)}}
	}
}

// --- Main ---

func main() {
	// 1. Config読み込み
	serverData, err := ioutil.ReadFile("server_config.yaml")
	if err != nil {
		log.Fatalf("Config Error: %v", err)
	}
	yaml.Unmarshal(serverData, &config.Server)

	mappingData, err := ioutil.ReadFile("mapping.yaml")
	if err != nil {
		log.Fatalf("Mapping Error: %v", err)
	}
	yaml.Unmarshal(mappingData, &config.Mapping)

	// デフォルト値
	if config.Server.MapRefreshIntervalSec <= 0 {
		config.Server.MapRefreshIntervalSec = 300
	}

	// 2. SNMP Global Init (Map更新用)
	gosnmp.Default.Target = config.Server.SnmpTarget
	gosnmp.Default.Community = config.Server.Community
	gosnmp.Default.Version = gosnmp.Version2c
	gosnmp.Default.Timeout = 2 * time.Second
	if err := gosnmp.Default.Connect(); err != nil {
		log.Fatalf("SNMP Connect Error: %v", err)
	}
	defer gosnmp.Default.Conn.Close()
	log.Printf("SNMP Connected to %s", config.Server.SnmpTarget)

	// 3. サーバー起動準備
	updateInterfaceMap()
	go backgroundRefresher()

	// 4. gRPC Server
	addr := fmt.Sprintf(":%d", config.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	srv := NewServer()
	pb.RegisterGNMIServer(s, srv)
	reflection.Register(s)

	log.Printf("gNMI Server listening on %s", addr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
