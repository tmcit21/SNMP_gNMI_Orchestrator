from pygnmi.client import gNMIclient

class GNMI_Conn():
    def __init__(self, addr, port, user, pw, insecure):
        self.addr: str = addr
        self.port: int = port
        self.user: str = user
        self.pw: str = pw
        self.insecure: bool = insecure

    def get(self, path):
        try:
            with gNMIclient(
                target=(self.addr, self.port),
                username=self.user,
                password=self.pw,
                insecure=self.insecure
            ) as gc:
                result = gc.get(path=path, encoding="json_ietf")
                return result
        except:
            return Exception

    def capabilities(self):
        try:
            with gNMIclient(
                target=(self.addr, self.port),
                username=self.user,
                password=self.pw,
                insecure=self.insecure
            ) as gc:
                gc.capabilities()
                return True
        except:
            return False

    def subscribe(self, path):
            with gNMIclient(
                target=(self.addr, self.port),
                username=self.user,
                password=self.pw,
                insecure=self.insecure
            ) as gc:
                result = gc.subscribe()
                return result


if __name__ == "__main__":
    c = GNMI_Conn("172.31.254.2", 57401, "admin", "NokiaSrl1!", True)
    #print(c.get(path=["/lldp/interfaces/interface/neighbors/"]))
    print(c.set(path=["/interfaces/interface/config/"]))
