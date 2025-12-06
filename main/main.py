import tomllib
from explorer import sysdescr
from confmanager import enable
from save import store
from typing import Any
from concurrent.futures import ThreadPoolExecutor, as_completed
import asyncio
import ipaddress
import time

GNMI_FAILED_MESSAGE = "gNMI Configuration FAILED"
DEBUG = True

class Controller():
    def __init__(self, parameter, workers=50):
        self.paramater: dict = parameter
        self.workers: int = workers
        self.table: dict = {}
        # {'IPaddr': {'snmp_community': str, 'ssh_username': str, 'ssh_password': str, 'gnmi_port_secure': int, 'gnmi_port_insecure': int, 'gnmi_insecure': bool, 'gnmi_username': str, 'gnmi_password': str, 'chassis_MAC': str | None, 'nos': str}
        #self.capability_table: dict = {}
        self.nos_table: dict = {}

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

    def _check_single_host(self, ip: str, config: dict) -> tuple[str, dict | None]: #SysDescr取得
            s = sysdescr.Sysdescr(ip, config["snmp_community"])
            raw_val = s.get()
            parsed = s.parse_sysdescr(raw_val)
            return ip, parsed

    def addr_prune(self) -> None: # SNMP応答ないアドレスをself.tableから削除
        valid_hosts = {}
        with ThreadPoolExecutor(max_workers=self.workers) as executor:
            future_to_ip = {
                executor.submit(self._check_single_host, ip, config): ip
                for ip, config in self.table.items()
            }
            for future in as_completed(future_to_ip):
                ip, result = future.result()

                if result:
                    current_config = self.table[ip]
                    nos = {"nos": result}
                    current_config.update(nos)
                    valid_hosts[ip] = current_config
                else:
                    pass
        self.table = valid_hosts

    def waiting(self): #
        pass

    def _set_gnmi(self, ip: str, config: dict) -> tuple[str, str | None]:
        c = enable.Conn(ip_address=ip, gnmi_username=config["gnmi_username"], ssh_username=config["ssh_username"], gnmi_password=config["gnmi_password"], ssh_password=config["ssh_password"],
                        gnmi_port_secure=config["gnmi_port_secure"], gnmi_port_insecure=config["gnmi_port_insecure"], gnmi_insecure=config["gnmi_insecure"], nos=config["nos"])
        if c.available():
            pass
        else:
            result = c.set_gnmi(self.nos_table[config["nos"]])
            if not result:
                return ip, None
        conf = c.get_gnmi_conf()
        return ip, conf

    def enconf(self) -> None: # gNMI設定
        with ThreadPoolExecutor(max_workers=self.workers) as executor:
            stats = {
                executor.submit(self._set_gnmi, ip, config): ip
                for ip, config in self.table.items()
            }
            for done in as_completed(stats):
                ip, result = done.result()
                s = store.Store(debug=DEBUG)
                if result:
                    s.save_log(ip, result)
                else:
                    s.save_log(ip, GNMI_FAILED_MESSAGE)

    def match_conf(self) -> None: #NOSとそれに投入するgNMIのconfをdictで結びつけ
        for v in self.table.values():
            self.nos_table[v["nos"]] = None
        for k, v in self.paramater["nos"]["operation"].items():
            if k in self.nos_table:
                self.nos_table[k] = v

    def run(self) -> None:
        starttime = time.monotonic()
        self.read()
        self.addr_prune()
        self.match_conf()
        self.enconf()
        endtime = time.monotonic()
        endtime - starttime
        print(self.table)


if __name__ == '__main__':
    with open("../devices/devices.toml", mode='rb') as toml_file:
        toml_data: dict[str, Any] = tomllib.load(toml_file)
        d = Controller(parameter=toml_data)
        d.run()
