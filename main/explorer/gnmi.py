from pygnmi.client import gNMIclient
import socket
class GNMI():
    def __init__(self, ip_address, port, name, password, insecure, skip_verify):
        self.target: str = (ip_address, port)
        self.name: str = name
        self.password: str = password
        self.insecure: bool = insecure
        self.skip_verify: bool = skip_verify

    def available(self) -> bool:
        try:
            with gNMIclient(
                target=self.target,
                username=self.name,
                password=self.password,
                insecure=self.insecure,
                skip_verify=self.skip_verify,
                gnmi_timeout=3,
            ) as gc:
                gc.capabilities()
                return True
        except Exception:
            return False

    def get_gnmi_conf(self):
        try:
            with gNMIclient(
                target=self.target,
                username=self.name,
                password=self.password,
                insecure=self.insecure,
                skip_verify=self.skip_verify,
                gnmi_timeout=3,
            ) as gc:
                result = gc.get("/system/grpc-servers")
                return result
        except Exception:
            return False


if __name__ == "__main__":
    devices = GNMI(ip_address="172.31.254.2", port=57401, name="admin", password="NokiaSrl1!", insecure=True, skip_verify=True)
    d = devices.get_gnmi_conf()
    print(d)
