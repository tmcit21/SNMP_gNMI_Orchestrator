from pygnmi.client import gNMIclient
import socket
class GNMI_Access():
    def __init__(self, ip_address, port, name, password, insecure, skip_verify):
        self.target: str = (ip_address, port)
        self.name: str = name
        self.password: str = password
        self.insecure: bool = insecure
        self.skip_verify: bool = skip_verify

    def available(self) -> bool:
        try:
            # TCP connectivity check (Socket level)
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(2)
                if sock.connect_ex(self.target) != 0:
                    return False

            # Try gNMI capabilities
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


if __name__ == "__main__":
    devices = GNMI_Access(ip_address="172.31.254.2", port=57401, name="admin", password="NokiaSrl1!", insecure=True, skip_verify=True)
    d = devices.available()
    print(d)
