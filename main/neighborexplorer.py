# neighborexplorer.py
import asyncio
from pygnmi.client import gNMIclient

class NeighborExplorer:
    """
    gNMI で LLDP を取得し、ネットワークトポロジを構築するクラス
    """

    def __init__(self, timeout=5):
        self.timeout = timeout

    async def _get_lldp(self, device):
        """
        gNMI で LLDP 情報を取得（中核関数）
        """
        target = (device["ip_address"], 57401)  # insecure対応
        try:
            with gNMIclient(
                target=target,
                username=device["username"],
                password=device["password"],
                insecure=True,
                skip_verify=True,
                timeout=self.timeout
            ) as gc:

                result = gc.get(
                    path=["/lldp/interfaces/interface/neighbors/"],
                    encoding="json_ietf"
                )

                return result

        except Exception as e:
            print(f"LLDP get failed for {device['ip_address']}: {e}")
            return None

    def get_neighbors(self, device):
        """
        同期ラッパー（← controller から普通の関数として呼べる）
        """
        return asyncio.run(self._get_lldp(device))

    async def _get_myself(self, device):
        """
        gNMI で 自身の chassis-idを取得
        """
        target = (device["ip_address"], 57401)  # insecure対応
        try:
            with gNMIclient(
                target=target,
                username=device["username"],
                password=device["password"],
                insecure=True,
                skip_verify=True,
                timeout=self.timeout
            ) as gc:

                result = gc.get(
                    path=["/lldp/state/chassis-id"],
                    encoding="json_ietf"
                )

                return result

        except Exception as e:
            print(f"LLDP get failed for {device['ip_address']}: {e}")
            return None

    def get_myself(self, device):
        """
        同期ラッパー（← controller から普通の関数として呼べる）
        """
        return asyncio.run(self._get_myself(device))


    def parse_neighbors(self, lldp_result):
        """
        gNMI の get 結果を解析して、
        隣接デバイスリストだけを返す
        """
        neighbors = []
        if not lldp_result:
            return neighbors
        mess = lldp_result["notification"][0]["update"][0]["val"]["interface"]#[0]#["neighbor"][0]["chassis-id"]
        #neighbors = list(map(lambda x: x["neighbor"], mess))#[0]["chassis-id"], mess))
        for m in mess:
            for n in m["neighbors"]["neighbor"]:
                neighbors.append(n["id"])
        return neighbors

    def parse_myself(self, lldp_result):
        return lldp_result["notification"][0]["update"][0]["val"]


    def neighbor_devices(self, root_devices):
        """
        root_devices: gNMI 対応ノードのリスト
        """
        existing = set()
        known_devices = set()

        for root in root_devices:
            result = self.get_neighbors(root)
            result = self.parse_neighbors(result)
            for r in result:
                existing.add(r)
        for root in root_devices:
            result = self.get_myself(root)
            result = self.parse_myself(result)
            known_devices.add(result)
        return existing - known_devices



if __name__ == "__main__":
    # 例: protocolcheck で gNMI OK と判定されたノード
    gnmi_nodes = [
        #Vyos|AA:C1:AB:2D:44:9C, SRL2|1A:10:02:FF:00:00, SRL2|1A:10:02:FF:00:00, SRL3|1A:FC:03:FF:00:00
        {'ip_address': '172.31.254.2', 'community': 'public', 'username': 'admin', 'password': 'NokiaSrl1!'}, #srl1
        #SRL1|1A:D7:01:FF:00:00, SRL1|1A:D7:01:FF:00:00, SRL3|1A:FC:03:FF:00:00
        {'ip_address': '172.31.254.3', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, #srl2
        #SRL1|1A:D7:01:FF:00:00, SRL2|1A:10:02:FF:00:00
        {'ip_address': '172.31.254.4', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'} #srl3
        ]


    nx = NeighborExplorer()
    topo = nx.neighbor_devices(gnmi_nodes)
    print(topo)
