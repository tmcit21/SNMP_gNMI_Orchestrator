from pygnmi.client import gNMIclient
from puresnmp import Client, V2C
import grpc
from concurrent.futures import TimeoutError
import socket

class ProtocolCheck:
    def __init__(self, targets):
        self.targets: list = targets
        self.inventory: dict = {}

    def run(self):
        for t in self.targets:
            if not self.snmp_available(t):
                self.inventory["unknown"] = t
                continue
            if self.insecure_device(t):
                b = self.gnmi_available(t, 57401)  # targetも渡す
            else:
                b = self.gnmi_available(t, 57400)  # targetも渡す
            if b:
                self.inventory["available"] = t
            else:
                self.inventory["unavailable"] = t

    def gnmi_available(self, target, port):
        """
        gNMIの接続可能性を確認する
        Args:
            target: 接続先の情報を含む辞書
            port: 接続ポート
        Returns:
            bool: 接続可能な場合True、不可能な場合False
        """
        try:
            # まず基本的な接続性をチェック
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(3)
                result = sock.connect_ex((target["ip_address"], port))
                if result != 0:
                    print(f"Port {port} is not accessible")
                    return False

            # gNMIクライアントでの接続試行
            with gNMIclient(
                target=(target["ip_address"], port),
                username=target["username"],
                password=target["password"],
                insecure=True,
                skip_verify=True,
                gnmi_timeout=3
            ) as gc:
                try:
                    capabilities = gc.capabilities()
                    print(f"Successfully connected to {target['ip_address']}:{port}")
                    return True
                except Exception as inner_e:
                    print(f"gNMI capabilities error: {type(inner_e).__name__}: {inner_e}")
                    return False

        except (grpc.FutureTimeoutError, grpc.RpcError) as grpc_e:
            print(f"gRPC error: {type(grpc_e).__name__}: {grpc_e}")
            return False
        except (TimeoutError, socket.timeout) as timeout_e:
            print(f"Timeout error: {type(timeout_e).__name__}: {timeout_e}")
            return False
        except ConnectionRefusedError as conn_e:
            print(f"Connection refused: {conn_e}")
            return False
        except OSError as os_e:
            print(f"OS error (network): {os_e}")
            return False
        except Exception as e:
            print(f"Unexpected error: {type(e).__name__}: {e}")
            import traceback
            traceback.print_exc()
            return False

    def snmp_available(self, target):
        try:
            client = Client(target["ip_address"], V2C(target["community"]))
            output = client.get("1.3.6.1.2.1.1.1.0")
            if isinstance(output, bytes):
                output = output.decode("utf-8", errors="replace")
            output = str(output)
            return True
        except Exception as e:
            print(e)
            return False

    def insecure_device(self, target):
        return True
