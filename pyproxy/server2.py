import time
import logging
from concurrent import futures
import asyncio

import grpc
import gnmi_pb2
import gnmi_pb2_grpc

# pysnmp 関連のインポート
from pysnmp.hlapi.asyncio import *
# ログ設定
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(message)s')
logger = logging.getLogger()

# --- SNMP ヘルパークラス ---
class SNMPClient:
    def __init__(self, target_ip='127.0.0.1', community='public'):
        self.target_ip = target_ip
        self.community = community

        # デモ用: インターフェース名とSNMP Indexのマッピング
        self.if_index_map = {
            "eth0": 2,
            "lo": 1
        }

    async def _get_oid_async(self, oid_str):
        """非同期でSNMP GETを実行する内部メソッド"""
        try:
            # pysnmp の async 版 getCmd を実行
            errorIndication, errorStatus, errorIndex, varBinds = await get_cmd(
                SnmpEngine(),
                CommunityData(self.community, mpModel=1), # SNMP v2c
                UdpTransportTarget((self.target_ip, 161), timeout=1, retries=0),
                ContextData(),
                ObjectType(ObjectIdentity(oid_str))
            )

            if errorIndication:
                logger.error(f"SNMP Error: {errorIndication}")
                return None
            elif errorStatus:
                logger.error(f"SNMP Error Status: {errorStatus.prettyPrint()}")
                return None
            else:
                val = varBinds[0][1]
                logger.info(f"SNMP GOT: {oid_str} -> {val.prettyPrint()}")
                return val
        except Exception as e:
            logger.error(f"SNMP Exception: {e}")
            return None

    def get_oid(self, oid_str):
        """外部から呼ばれる同期ラッパーメソッド"""
        # 現在のコンテキストで一時的にイベントループを作成して実行
        try:
            return asyncio.run(self._get_oid_async(oid_str))
        except Exception as e:
            logger.error(f"Asyncio Execution Error: {e}")
            return None

# --- gNMI サーバー実装 ---
class gNMIServicer(gnmi_pb2_grpc.gNMIServicer):

    def __init__(self):
        self.snmp = SNMPClient(target_ip='127.0.0.1', community='public')

    def Capabilities(self, request, context):
        return gnmi_pb2.CapabilityResponse(
            supported_models=[],
            supported_encodings=[gnmi_pb2.JSON],
            gNMI_version="0.7.0"
        )

    def Get(self, request, context):
        # 1回のGetRequestで複数のPathが来る可能性がありますが、簡単のため1つ目を処理
        path_obj = request.path[0]
        logger.info(f"Received Get Request for path: {path_obj}")

        # パスの解析 (簡易版)
        path_elements = [e.name for e in path_obj.elem]
        path_str = "/".join(path_elements) # 例: system/state/description

        # レスポンス用変数の初期化
        gnmi_val = None

        # --- マッピングロジック ---

        # パターン1: System Description
        if path_str == "system/state/description":
            oid = "1.3.6.1.2.1.1.1.0" # sysDescr
            snmp_result = self.snmp.get_oid(oid)
            if snmp_result is not None:
                # SNMPのOctetStringをgNMIのStringValに変換
                gnmi_val = gnmi_pb2.TypedValue(string_val=str(snmp_result))

        # パターン2: Interface Counters (例: interfaces/interface[name=eth0]/state/counters/in-octets)
        elif path_str == "interfaces/interface/state/counters/in-octets":
            # キー(name=eth0) を抽出
            if_name = None
            for elem in path_obj.elem:
                if "name" in elem.key:
                    if_name = elem.key["name"]
                    break

            if if_name and if_name in self.snmp.if_index_map:
                idx = self.snmp.if_index_map[if_name]
                oid = f"1.3.6.1.2.1.2.2.1.10.{idx}" # ifInOctets.Index

                snmp_result = self.snmp.get_oid(oid)
                if snmp_result is not None:
                    # SNMPのInteger/CounterをgNMIのIntValに変換
                    gnmi_val = gnmi_pb2.TypedValue(int_val=int(snmp_result))
            else:
                logger.warning(f"Interface {if_name} not found in map")

        # --- レスポンス構築 ---

        if gnmi_val:
            return gnmi_pb2.GetResponse(
                notification=[
                    gnmi_pb2.Notification(
                        timestamp=int(time.time() * 1e9),
                        update=[
                            gnmi_pb2.Update(
                                path=path_obj,
                                val=gnmi_val
                            )
                        ]
                    )
                ]
            )
        else:
            # 値が取れなかった場合 (エラーではなく空を返すか、エラーにするかは設計次第)
            # ここでは空のNotificationを返す
            return gnmi_pb2.GetResponse(notification=[])

    def Set(self, request, context):
        # 今回はSetは未実装（ログのみ）
        logger.info("Set request received (Not implemented for SNMP proxy)")
        return gnmi_pb2.SetResponse(timestamp=int(time.time() * 1e9))

    def Subscribe(self, request_iterator, context):
        # Subscribeも今回は省略
        context.abort(grpc.StatusCode.UNIMPLEMENTED, "Subscribe not implemented in this demo")

def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    gnmi_pb2_grpc.add_gNMIServicer_to_server(gNMIServicer(), server)
    port = '[::]:50051'
    server.add_insecure_port(port)
    logger.info(f"gNMI-SNMP Proxy Server listening on {port}")
    server.start()
    try:
        server.wait_for_termination()
    except KeyboardInterrupt:
        server.stop(0)

if __name__ == '__main__':
    serve()
