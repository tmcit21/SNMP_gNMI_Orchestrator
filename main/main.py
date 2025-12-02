import tomllib
from explorer import gnmi, sysdescr
from typing import Any
from concurrent.futures import ThreadPoolExecutor, as_completed
import asyncio
import ipaddress
import time

class Controller():
    def __init__(self, parameter, workers=50):
        self.paramater: dict = parameter
        self.workers: int = workers
        self.table: dict = {}
        # {'IPaddr': {'snmp_community': str, 'ssh_username': str, 'ssh_password': str, 'gnmi_port_secure': int, 'gnmi_port_insecure': int, 'gnmi_insecure': bool, 'gnmi_username': str, 'gnmi_password': str, 'chassis_MAC': str | None, 'vendor': str, 'nos': str, 'raw': str}
        #self.capability_table: dict = {}

    def read(self) -> None: # 設定読込
        subnet = ipaddress.ip_network(self.paramater["global"]["network"], strict=False)
        ip_list = set(str(ip) for ip in subnet.hosts()) - set(self.paramater["global"]["exclude"])

        keys = ["snmp_community", "ssh_username", "ssh_password", "gnmi_port_secure",
                "gnmi_port_insecure", "gnmi_insecure", "gnmi_username", "gnmi_password", "chassis_MAC"]

        self.table = {}
        for ip in ip_list:
            self.table[ip] = {k: None for k in keys}
            for k in keys[:-1]:
                self.table[ip][k] = self.paramater["default"][k]

        for ip, config in self.paramater["devices"].items():
            if ip in self.table:
                for c, v in config.items():
                    self.table[ip][c] = v

    def _check_single_host(self, ip: str, config: dict) -> tuple[str, dict | None]:
            s = sysdescr.Sysdescr(ip, config["snmp_community"])
            raw_val = s.get()
            parsed = s.parse_sysdescr(raw_val)
            if parsed and parsed['nos'] != 'Unknown':
                return ip, parsed
            return ip, None

    def addr_prune(self) -> None:
        valid_hosts = {}
        with ThreadPoolExecutor(max_workers=self.workers) as executor:
            future_to_ip = {
                executor.submit(self._check_single_host, ip, config): ip
                for ip, config in self.table.items()
            }
            for future in as_completed(future_to_ip):
                ip, result = future.result()

                if result:
                    #print(f"[FOUND] {ip}: {result['nos']}")
                    current_config = self.table[ip]
                    current_config.update(result)
                    valid_hosts[ip] = current_config
                else:
                    pass
        self.table = valid_hosts


    def ssh_save_conf(self, addr: str, ssh_name: str, ssh_password: str) -> bool: #gNMI有効化前に設定保存
        pass

    def gnmi_save_conf(self, addr: str, ssh_name: str, ssh_password: str) -> bool: # /system/grpc-server/(うろ覚え)以下保存
        pass

    def command_check(self, addr: str, commands: list) -> bool: #gNMI有効化コマンド流し込み
        pass

    def capability_check(self, addr: str, gnmi_name: str, gnmi_password: str, port: int, insecure: bool, skip_verify: bool) -> bool: #gNMI capabilities取得
        return gnmi.GNMI(ip_address=addr, port=port, name=gnmi_name, password=gnmi_password, insecure=insecure, skip_verify=skip_verify).available()

    def set_gnmi_client(self, ): # gNMI Clientの設定
        pass

    def get_snmp_request(self, ): # SNMP Managerが収集しているOIDを取得
        pass

    def set_collect_path(self, ): # SNMP GET -> gNMI GETしたいパスを設定
        pass

    def save_log(self):
        pass

    def waiting(self): #
        pass

    def run(self) -> None:
        self.read()
        self.addr_prune()
        print(self.table)


if __name__ == '__main__':
    with open("../devices/devices.toml", mode='rb') as toml_file:
        toml_data: dict[str, Any] = tomllib.load(toml_file)
        d = Controller(parameter=toml_data)
        d.run()
