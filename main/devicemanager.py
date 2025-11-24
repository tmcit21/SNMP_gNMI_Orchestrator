class DeviceManager:
    """
    ProtocolCheck の結果（inventory）を元に
    デバイスを分類するクラス
    """

    def __init__(self, inventory: dict):
        self.inventory = inventory
        self.devices = {
            "snmp_only": [],
            "gnmi_supported": [],
            "unknown": []
        }

    def classify(self):
        """
        inventory の内容をもとに分類を行う
        """

        # gNMI OK → gNMI supported
        for dev in self.inventory.get("available", []):
            self.devices["gnmi_supported"].append(dev)

        # SNMP OK + gNMI NG → snmp only
        for dev in self.inventory.get("unavailable", []):
            self.devices["snmp_only"].append(dev)

        # SNMP NG → unknown
        for dev in self.inventory.get("unknown", []):
            self.devices["unknown"].append(dev)

        return self.devices

    def summary(self):
        """
        簡単な統計情報を返す（デバッグ・ログ用）
        """
        return {
            "total": sum(len(lst) for lst in self.devices.values()),
            "snmp_only": len(self.devices["snmp_only"]),
            "gnmi_supported": len(self.devices["gnmi_supported"]),
            "unknown": len(self.devices["unknown"])
        }


if __name__ == "__main__":
    from protocolcheck import ProtocolCheck
    targets = [
        {"ip_address": "172.31.254.1", "community": "public", "username": "admin", "password": "NokiaSrl1!"},
        {"ip_address": "172.31.254.2", "community": "public", "username": "admin", "password": "NokiaSrl1!"},
        {'ip_address': '172.31.254.1', 'username': 'admin', 'password': 'admin', 'community': 'public'},
        {'ip_address': '172.31.254.254', 'username': 'clab', 'password': 'clab@123', 'community': 'public'}
    ]

    pc = ProtocolCheck(targets=targets)
    pc.run()

    dm = DeviceManager(pc.inventory)
    result = dm.classify()

    print(result)
    print(dm.summary())
