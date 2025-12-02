from pygnmi.client import gNMIclient
from puresnmp import Client, V2C, ObjectIdentifier as OID
import socket
from concurrent.futures import ThreadPoolExecutor, as_completed
import asyncio

class ProtocolCheck:
    def __init__(self, targets=None, workers=50):
        self.targets = targets if targets is not None else []
        self.workers = workers
        self.inventory = {
            "available": [],
            "unavailable": [],
            "unknown": []
        }

    def run(self):
        with ThreadPoolExecutor(max_workers=self.workers) as executor:
            futures = {executor.submit(self.check_target, t): t for t in self.targets}

            for future in as_completed(futures):
                t = futures[future]
                try:
                    status = future.result()

                    if status == "unknown":
                        self.inventory["unknown"].append(t)
                    elif status == "available":
                        self.inventory["available"].append(t)
                    elif status == "unavailable":
                        self.inventory["unavailable"].append(t)

                except Exception as e:
                    self.inventory["unknown"].append(t)

    def check_target(self, t: dict):
        # SNMPが通らなければ unknown 扱いにして終了
        if not self.snmp_available(t):
            return "unknown"

        # SNMPが通るなら gNMI のチェックへ
        port = 57401 if self.insecure_device(t) else 57400

        if self.gnmi_available(t, port):
            return "available"
        else:
            return "unavailable"

    def snmp_available(self, target):
            async def _check():
                TIMEOUT = 2.0
                target_oid = "1.3.6.1.2.1.1.1.0"
                client = Client(target["ip_address"], V2C(target["community"]))
                await asyncio.wait_for(
                    client.get(OID(target_oid)),
                    timeout=TIMEOUT
                )
            try:
                asyncio.run(_check())
                return True
            except Exception as e:
                return False

    def gnmi_available(self, target, port):
        try:
            # TCP connectivity check (Socket level)
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(2)
                if sock.connect_ex((target["ip_address"], port)) != 0:
                    return False

            # Try gNMI capabilities
            with gNMIclient(
                target=(target["ip_address"], port),
                username=target["username"],
                password=target["password"],
                insecure=True,
                skip_verify=True,
                gnmi_timeout=3
            ) as gc:
                gc.capabilities()
                return True

        except Exception:
            return False

    def insecure_device(self, target):
        """
        暗号化対応は後回しなのでTrue
        """
        return True


if __name__ == "__main__":
    devices = ProtocolCheck(targets=[
        # SNMP・gNMI対応
        {'ip_address': '172.31.254.2', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'},
        {'ip_address': '172.31.254.3', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'},
        # 非対応 / 到達不可 (想定)
        {'ip_address': '172.31.254.1', 'username': 'admin', 'password': 'admin', 'community': 'public'},
        {'ip_address': '172.31.254.254', 'username': 'clab', 'password': 'clab@123', 'community': 'public'}
    ])
    devices.run()
    import pprint
    pprint.pprint(devices.inventory)
    """
    {'available': [{'community': 'public',
                'ip_address': '172.31.254.3',
                'password': 'NokiaSrl1!',
                'username': 'admin'},
               {'community': 'public',
                'ip_address': '172.31.254.2',
                'password': 'NokiaSrl1!',
                'username': 'admin'}],
 'unavailable': [{'community': 'public',
                  'ip_address': '172.31.254.1',
                  'password': 'admin',
                  'username': 'admin'}],
 'unknown': [{'community': 'public',
              'ip_address': '172.31.254.254',
              'password': 'clab@123',
              'username': 'clab'}]}
    """
