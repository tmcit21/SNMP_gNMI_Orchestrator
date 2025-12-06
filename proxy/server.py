import time
import logging
import yaml
import asyncio
from concurrent import futures

import grpc
import gnmi_pb2
import gnmi_pb2_grpc
from pysnmp.hlapi.asyncio import *

# ログ設定
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(message)s')
logger = logging.getLogger()

CONFIG_FILE = "mapping.yaml"

class SNMPClient:
    def __init__(self, if_map, target_ip='127.0.0.1', community='public'):
        self.target_ip = target_ip
        self.community = community
        self.if_index_map = if_map

    async def _get_oid_async(self, oid_str):
        try:
            # pysnmp v6/Python3.12対応: create()を使用
            transport = await UdpTransportTarget.create((self.target_ip, 161))

            errorIndication, errorStatus, errorIndex, varBinds = await get_cmd(
                SnmpEngine(),
                CommunityData(self.community, mpModel=1),
                transport,
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
                return varBinds[0][1]
        except Exception as e:
            logger.error(f"SNMP Exception: {e}")
            return None

    def get_oid(self, oid_str):
        try:
            return asyncio.run(self._get_oid_async(oid_str))
        except Exception as e:
            logger.error(f"Asyncio Execution Error: {e}")
            return None

class gNMIServicer(gnmi_pb2_grpc.gNMIServicer):

    def __init__(self):
        # 起動時にYAMLファイルを読み込む
        self.load_config()
        # SNMPクライアントの初期化 (YAMLからインターフェースマップを渡す)
        self.snmp = SNMPClient(
            if_map=self.config.get('interface_map', {}),
            target_ip='127.0.0.1',
            community='public'
        )

    def load_config(self):
        try:
            with open(CONFIG_FILE, 'r') as f:
                self.config = yaml.safe_load(f)
            logger.info(f"Loaded configuration from {CONFIG_FILE}")
        except Exception as e:
            logger.error(f"Failed to load config file: {e}")
            self.config = {"paths": {}, "interface_map": {}}

    def _parse_gnmi_path(self, path_obj):
        """
        gNMIのPathオブジェクトを解析し、
        1. 検索用パス文字列 (例: /interfaces/interface/state/counters/in-octets)
        2. キー情報 (例: {"name": "eth0"})
        を返す
        """
        path_parts = []
        keys = {}

        for elem in path_obj.elem:
            path_parts.append(elem.name)
            if elem.key:
                for k, v in elem.key.items():
                    keys[k] = v

        # 先頭に / をつける
        path_str = "/" + "/".join(path_parts)
        return path_str, keys

    def Capabilities(self, request, context):
        return gnmi_pb2.CapabilityResponse(
            supported_models=[],
            supported_encodings=[gnmi_pb2.JSON],
            gNMI_version="0.7.0"
        )

    def Get(self, request, context):
        # 1つのGetRequestに含まれる最初のパスのみ処理（簡略化）
        if not request.path:
            return gnmi_pb2.GetResponse()

        gnmi_path_obj = request.path[0]

        # パスを解析して文字列化
        req_path_str, req_keys = self._parse_gnmi_path(gnmi_path_obj)
        logger.info(f"Request Path: {req_path_str}, Keys: {req_keys}")

        # YAMLのマッピング定義から検索
        mapping = self.config['paths'].get(req_path_str)

        val_response = None

        if mapping:
            target_oid = None

            # --- OIDの決定ロジック ---
            if mapping.get('requires_index'):
                # インターフェースIndexが必要な場合
                if 'name' in req_keys:
                    if_name = req_keys['name']
                    if_index = self.snmp.if_index_map.get(if_name)

                    if if_index:
                        target_oid = f"{mapping['oid_base']}.{if_index}"
                    else:
                        logger.warning(f"Interface '{if_name}' not found in mapping")
                else:
                    logger.warning("Path requires index but no key provided")
            else:
                # シンプルなScalar値の場合
                target_oid = mapping.get('oid')

            # --- SNMP実行 ---
            if target_oid:
                snmp_val = self.snmp.get_oid(target_oid)

                if snmp_val is not None:
                    # --- 型変換ロジック ---
                    type_def = mapping.get('type', 'string')

                    if type_def == 'int':
                        # SNMPの結果を数値として扱う
                        try:
                            val_response = gnmi_pb2.TypedValue(int_val=int(snmp_val))
                        except:
                             logger.error(f"Failed to cast {snmp_val} to int")
                    else:
                        # デフォルトは文字列
                        val_response = gnmi_pb2.TypedValue(string_val=str(snmp_val))
        else:
            logger.warning(f"No mapping found for path: {req_path_str}")

        # --- レスポンス構築 ---
        if val_response:
            return gnmi_pb2.GetResponse(
                notification=[
                    gnmi_pb2.Notification(
                        timestamp=int(time.time() * 1e9),
                        update=[
                            gnmi_pb2.Update(
                                path=gnmi_path_obj,
                                val=val_response
                            )
                        ]
                    )
                ]
            )
        else:
            return gnmi_pb2.GetResponse(notification=[])

    def Set(self, request, context):
        return gnmi_pb2.SetResponse(timestamp=int(time.time() * 1e9))

    def Subscribe(self, request_iterator, context):
        context.abort(grpc.StatusCode.UNIMPLEMENTED, "Not implemented")

def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    gnmi_pb2_grpc.add_gNMIServicer_to_server(gNMIServicer(), server)
    port = '[::]:50051'
    server.add_insecure_port(port)
    logger.info(f"Generic gNMI-SNMP Server listening on {port}")
    server.start()
    try:
        server.wait_for_termination()
    except KeyboardInterrupt:
        server.stop(0)

if __name__ == '__main__':
    serve()
