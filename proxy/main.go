package main

import (
	"context"
	"log"
	"net"
	"time"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Server は gNMI サーバーのインターフェースを実装する構造体です
type Server struct {
	pb.UnimplementedGNMIServer
}

// Capabilities はサーバーの対応バージョンやエンコーディングを返します
func (s *Server) Capabilities(ctx context.Context, req *pb.CapabilityRequest) (*pb.CapabilityResponse, error) {
	log.Println("Received Capabilities Request")
	return &pb.CapabilityResponse{
		SupportedModels:    []*pb.ModelData{},
		SupportedEncodings: []pb.Encoding{pb.Encoding_JSON_IETF},
		GNMIVersion:        "0.7.0",
	}, nil
}

// Get は特定パスのデータを返します (今回はダミーデータを返却)
func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	log.Printf("Received Get Request for paths: %v", req.Path)

	// ダミーの現在時刻を返す
	now := time.Now().String()

	return &pb.GetResponse{
		Notification: []*pb.Notification{
			{
				Timestamp: time.Now().UnixNano(),
				Update: []*pb.Update{
					{
						Path: &pb.Path{
							Elem: []*pb.PathElem{{Name: "system"}, {Name: "clock"}},
						},
						Val: &pb.TypedValue{
							Value: &pb.TypedValue_StringVal{StringVal: now},
						},
					},
				},
			},
		},
	}, nil
}

// Set は設定変更を受け付けます (ログ出力のみ実施)
func (s *Server) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	log.Println("Received Set Request")

	// Update, Replace, Delete の内容をログに出力
	for _, u := range req.Update {
		log.Printf("Update: Path=%v, Val=%v", u.Path, u.Val)
	}
	for _, r := range req.Replace {
		log.Printf("Replace: Path=%v, Val=%v", r.Path, r.Val)
	}
	for _, d := range req.Delete {
		log.Printf("Delete: Path=%v", d)
	}

	return &pb.SetResponse{
		Timestamp: time.Now().UnixNano(),
		// 各操作に対する結果をここに詰めるのが正式ですが、雛形として省略して成功とみなします
	}, nil
}

// Subscribe はストリーミングでデータを送信します
func (s *Server) Subscribe(stream pb.GNMI_SubscribeServer) error {
	log.Println("Received Subscribe Request")

	// クライアントからの初期リクエストを受信
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	log.Printf("Subscribe Mode: %v", req.GetSubscribe().Mode)

	// Streamモードの場合、定期的にデータを送信するデモ
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stream.Context().Done():
			log.Println("Client disconnected")
			return nil
		case t := <-ticker.C:
			// 2秒ごとにダミー更新通知を送信
			log.Println("Sending Update...")
			response := &pb.SubscribeResponse{
				Response: &pb.SubscribeResponse_Update{
					Update: &pb.Notification{
						Timestamp: t.UnixNano(),
						Update: []*pb.Update{
							{
								Path: &pb.Path{
									Elem: []*pb.PathElem{{Name: "counter"}},
								},
								Val: &pb.TypedValue{
									Value: &pb.TypedValue_IntVal{IntVal: int64(t.Second())},
								},
							},
						},
					},
				},
			}
			if err := stream.Send(response); err != nil {
				return err
			}
		}
	}
}

func main() {
	port := ":50051"
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()

	// gNMIサーバーを登録
	pb.RegisterGNMIServer(s, &Server{})

	// gRPC CLI等でのデバッグ用にリフレクションを有効化
	reflection.Register(s)

	log.Printf("gNMI Server listening on %s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
