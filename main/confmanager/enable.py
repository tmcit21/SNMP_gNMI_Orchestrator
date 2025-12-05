import os
import sys
from netmiko import BaseConnection
# mainディレクトリをパスに追加
current_dir = os.path.dirname(os.path.abspath(__file__))
main_dir = os.path.dirname(current_dir)
if main_dir not in sys.path:
    sys.path.insert(0, main_dir)

# 絶対インポート
from explorer.sysdescr import SNMP_MAPPER

SH_CONF = dict.fromkeys(SNMP_MAPPER, None)

class Conn:
    def __init__(self, ip_address, gnmi_username, ssh_username, gnmi_password, ssh_password, gnmi_port_secure,
        gnmi_port_insecure, gnmi_insecure, nos):
        self.ip_address = ip_address
        self.gnmi_username = gnmi_username
        self.ssh_username = ssh_username
        self.gnmi_password = gnmi_password
        self.ssh_password = ssh_password
        self.gnmi_port_secure = gnmi_port_secure
        self.gnmi_port_insecure = gnmi_port_insecure
        self.gnmi_insecure = gnmi_insecure
        self.nos = nos

    def set_gnmi(self, commands: list):
        c = BaseConnection(device_type=self.nos, ip=self.ip_address, username=self.ssh_username, password=self.ssh_password, port=22)
        try:
            for command in commands:
                c.send_command(command)
            return True
        except:
            return False

if __name__ == "__main__":
    print(SH_CONF)
    c = Conn(ip_address="172.31.254.2", gnmi_username="admin", ssh_username="admin", gnmi_password="NokiaSrl1!", ssh_password="NokiaSrl1!",
        gnmi_port_insecure=57401, gnmi_port_secure=57400, gnmi_insecure=True, nos="nokia_srl")
    print(c.set_gnmi([
    "enter candidate",
    "set /system grpc-server insecure-mgmt admin-state enable",
    "set /system grpc-server insecure-mgmt rate-limit 65000",
    "set /system grpc-server insecure-mgmt yang-models openconfig",
    "set /system grpc-server insecure-mgmt network-instance mgmt",
    "set /system grpc-server insecure-mgmt port 57401",
    "set /system grpc-server insecure-mgmt ",
    "set /system grpc-server insecure-mgmt admin-state enable",
    "set /system grpc-server insecure-mgmt admin-state enable",
    "set /system grpc-server insecure-mgmt admin-state enable",
    "set /system grpc-server insecure-mgmt admin-state enable",
    ]))
