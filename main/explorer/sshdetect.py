from netmiko import ConnectHandler
import netmiko.ssh_autodetect as ssa
# SSHDetectはまだ対応していないNOSもあるのでここに追加
ssa.SSH_MAPPER_DICT["nokia_srl"] = {
        "cmd": "show version",
        "search_patterns": [r"SR Linux"],
        "priority": 99,
        "dispatch": "_autodetect_std",
    }
ssa.SSH_MAPPER_DICT["vyos"] = {
        "cmd": "show system commit",
        "search_patterns": [r"vyos"],
        "priority": 99,
        "dispatch": "_autodetect_std",
    }


class SSH():
    def __init__(self, device_type: str, ip_address: str, ssh_user: str, ssh_password: str):
        self.device_type = device_type
        self.ip_address = ip_address
        self.ssh_user = ssh_user
        self.ssh_password = ssh_password
        self.manual = {"device_type": self.device_type, "host": self.ip_address, "username": self.ssh_user, "password": self.ssh_password}
        self.auto = {"device_type": "autodetect", "host": self.ip_address, "username": self.ssh_user, "password": self.ssh_password}


    #def conn(self):
    #    net_connect = ConnectHandler(**self.manual)#ConnectHandler(device_type=self.device_type, host=self.ip_address, username=self.ssh_user, password=self.ssh_password)
    #    net_connect.find_prompt()

    def sshautoconn(self):
        guesser = ssa.SSHDetect(**self.auto, conn_timeout=3, banner_timeout=3, blocking_timeout=5, session_timeout=5)
        print(guesser.autodetect())

    #def shmpautoconn(self):
    #    guesser = SNMPDetect(**self.auto)
    #    print(guesser.autodetect())

if __name__ == "__main__":
    devices = SSH(device_type="nokia_srl", ip_address="172.31.254.2", ssh_user="admin", ssh_password="NokiaSrl1!")
    #d = devices.conn()
    #print(d)
    devices = SSH(device_type="autodetect", ip_address="172.31.254.2", ssh_user="admin", ssh_password="NokiaSrl1!")
    #devices.sshautoconn()
    print(ssa.SSH_MAPPER_DICT)
    print("\n")
    #devices = SSH(device_type="autodetect", ip_address="172.31.254.5", ssh_user="vyos", ssh_password="vyos")
    devices.sshautoconn()

"""
    "nokia_srl": {
        "cmd": "show version",
        "search_patterns": [r"SR Linux"],
        "priority": 99,
        "dispatch": "_autodetect_std",
    },
        "vyos": {
        "cmd": "show system commit",
        "search_patterns": [r"vyos"],
        "priority": 99,
        "dispatch": "_autodetect_std",
    },

"""
