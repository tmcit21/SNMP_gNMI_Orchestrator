import time
import logging
from concurrent import futures

import grpc
import gnmi_pb2
import gnmi_pb2_grpc

# ログ設定
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(message)s')
logger = logging.getLogger()

class gNMIServicer(gnmi_pb2_grpc.gNMIServicer):

    def Capabilities(self, request, context):
            logger.info("Received Capabilities Request")
            return gnmi_pb2.CapabilityResponse(
                supported_models=[],
                supported_encodings=[gnmi_pb2.JSON]
                #gNMI_version="0.7.0"
            )

    def Get(self, request, context):
        logger.info(f"Received Get Request: {request.path}")

        now = str(time.time())

        return gnmi_pb2.GetResponse(
            notification=[
                gnmi_pb2.Notification(
                    timestamp=int(time.time() * 1e9),
                    update=[
                        gnmi_pb2.Update(
                            path=gnmi_pb2.Path(
                                elem=[
                                    gnmi_pb2.PathElem(name="system"),
                                    gnmi_pb2.PathElem(name="clock")
                                ]
                            ),
                            val=gnmi_pb2.TypedValue(string_val=now)
                        )
                    ]
                )
            ]
        )

    def Set(self, request, context):
        logger.info("Received Set Request")

        # リクエスト内容のログ出力
        for update in request.update:
            logger.info(f"Update: Path={update.path}, Val={update.val}")
        for replace in request.replace:
            logger.info(f"Replace: Path={replace.path}, Val={replace.val}")
        for delete in request.delete:
            logger.info(f"Delete: Path={delete}")

        return gnmi_pb2.SetResponse(
            timestamp=int(time.time() * 1e9)
        )

    def Subscribe(self, request_iterator, context):
        logger.info("Received Subscribe Request")

        # ストリームの最初のリクエストを取得（ここでモード確認）
        try:
            first_request = next(request_iterator)
            mode = first_request.subscribe.mode
            logger.info(f"Subscribe Mode: {mode}")
        except StopIteration:
            return
        while context.is_active():
            t = int(time.time())
            logger.info("Sending Update...")

            # レスポンス作成
            response = gnmi_pb2.SubscribeResponse(
                update=gnmi_pb2.Notification(
                    timestamp=t * 1000000000,
                    update=[
                        gnmi_pb2.Update(
                            path=gnmi_pb2.Path(
                                elem=[gnmi_pb2.PathElem(name="counter")]
                            ),
                            val=gnmi_pb2.TypedValue(int_val=t % 60) # 秒数を送る
                        )
                    ]
                )
            )

            yield response
            time.sleep(2)

def serve():
    # gRPCサーバーの起動設定
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    gnmi_pb2_grpc.add_gNMIServicer_to_server(gNMIServicer(), server)

    port = '[::]:50051'
    server.add_insecure_port(port)
    logger.info(f"gNMI Server listening on {port}")

    server.start()
    try:
        server.wait_for_termination()
    except KeyboardInterrupt:
        server.stop(0)

if __name__ == '__main__':
    serve()
