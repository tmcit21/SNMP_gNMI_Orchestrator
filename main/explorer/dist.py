from netmiko import ConnectHandler, file_transfer
import time

ARCH_MAP = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "aarch64": "arm64",
    "arm64": "arm64",
}

SHELL_ENTRY_COMMAND = [ #NOSのCLIからshellに入るためのコマンド
    "",
    "bash",
    "start shell",
    "system shell",
    "request platform software system shell"
]

"""
Linuxコマンドが実行できる = バイナリが実行できるではないが、NOS名のパターンマッチより汎用的なのでこうした
"""


class Dist:
    def __init__(self, ip_address: str, ssh_username: str, ssh_password: str, nos: str):
        self.ip_address = ip_address
        self.ssh_username = ssh_username
        self.ssh_password = ssh_password
        self.nos = nos
        self.login = {"device_type": self.nos, "host": self.ip_address, "username": self.ssh_username, "password": self.ssh_password}
        self.arch = None
        self.path = None
        self.check_cmd = None
        self.capable = False

    def detect_capability(self) -> bool: #linuxコマンド通じるか
        try:
            c = ConnectHandler(**self.login)
            for cmd in SHELL_ENTRY_COMMAND:
                try:
                    if cmd:
                        c.send_command(cmd, expect_string=r"#|\$|%", delay_factor=2)
                    raw_arch = c.send_command("uname -m").strip()
                    if any(x in raw_arch.lower() for x in ["invalid", "syntax", "unknown", "error"]):
                        continue
                    self._cached_arch = ARCH_MAP.get(raw_arch, raw_arch)
                    raw_path = c.send_command("pwd").strip()
                    if not raw_path.endswith("/"):
                        raw_path += "/"
                    self._cached_path = raw_path
                    #将来のためにとっとく
                    self._entry_cmd = cmd
                    self._is_capable = True
                    c.disconnect()
                    return True
                except Exception:
                    continue
            c.disconnect()
            return False
        except Exception:
            return False

    def check_arch(self) -> str:
        if self.arch:
            return self.arch
        try:
            c = ConnectHandler(**self.login)
            arch = c.send_command("uname -m").strip()
            c.disconnect()
        except:
            return "unknown"
        self.arch = ARCH_MAP.get(arch, arch)
        return self.arch


    def check_path(self) -> str: #配置するパス #あとで実装!!!!!!
        if self.path:
            return self.path
        try:
            c = ConnectHandler(**self.login)
            abs_path = c.send_command("pwd").strip()
            c.disconnect()
            if not abs_path.endswith("/"):
                abs_path += "/"
            self.path = abs_path
            return self.path
        except:
            return f"/home/{self.ssh_username}/"
        #return "/config/scripts/"

    def remote_send(self, local_paths: list) -> bool: #配置
        file_system = self.check_path()
        try:
            c = ConnectHandler(**self.login)
            for local_path in local_paths:
                dest_file = local_path.rpartition('/')[2]
                file_transfer(c, source_file=local_path, dest_file=dest_file, file_system=file_system, direction="put", overwrite_file=True)
                c.send_command(f"chmod +x {file_system}{dest_file}")
            c.disconnect()
            return True
        except Exception:
            try: #悪あがき
                tmp = self.login["device_type"]
                self.login["device_type"] = "linux"
                c = ConnectHandler(**self.login)
                for local_path in local_paths:
                    dest_file = local_path.rpartition('/')[2]
                    file_transfer(c, source_file=local_path, dest_file=dest_file, file_system=file_system, direction="put", overwrite_file=True)
                    c.send_command(f"chmod +x {file_system}{dest_file}")
                c.disconnect()
                self.login["device_type"] = tmp
                return True
            except Exception:
                return False

    def remote_del(self, local_paths: list) -> bool: #replaceしてから削除
        file_system = self.check_path()
        try:
            c = ConnectHandler(**self.login)
            for local_path in local_paths:
                dest_file = local_path.rpartition('/')[2]
                c.send_command(f"rm {file_system + dest_file}")
            c.disconnect()
            return True
        except Exception:
            try: #悪あがき
                tmp = self.login["device_type"]
                self.login["device_type"] = "linux"
                c = ConnectHandler(**self.login)
                for local_path in local_paths:
                    dest_file = local_path.rpartition('/')[2]
                    c.send_command(f"rm {file_system + dest_file}")
                c.disconnect()
                self.login["device_type"] = tmp
                return True
            except Exception:
                return False

    def remote_run(self) -> bool:
        file_system = self.check_path()
        full_path = f"{file_system}gnmi-proxy-{self.check_arch()}"
        try:
            c = ConnectHandler(**self.login)
            c.send_command("sudo systemctl stop gnmi-proxy")
            c.send_command("sudo systemctl reset-failed gnmi-proxy")
            #何故かパス変わるから毎回移動
            real_cmd = f"cd {file_system} && {full_path}"
            cmd = f"sudo systemd-run --unit=gnmi-proxy --description='gNMI Proxy' /bin/sh -c '{real_cmd}'"
            c.send_command(cmd)
            time.sleep(2)
            check = c.send_command("systemctl is-active gnmi-proxy")
            if "active" in check:
                return True
            else:
                return False

        except Exception as e:
            return False
        """
        try:
            c = ConnectHandler(**self.login)
            c.send_command(f"sudo systemctl stop gnmi-proxy || true") #port占有しないように
            c.send_command(cmd)
            time.sleep(2)
            #check = c.send_command(f"pgrep -f {full_path}")
            check = c.send_command(f"systemctl is-active gnmi-proxy")
            print(check)
            c.disconnect()
            if "active" in check:
                print("Warning: Process might not have started.")
                return False
            else:
                print(f"Success! PID: {check.strip()}")
                return True
        except Exception as e:
            print(f"Run Error: {e}")
            return False
        """

    def remote_stop(self) -> bool:
        try:
            c = ConnectHandler(**self.login)
            c.send_command("sudo systemctl stop gnmi-proxy")
            c.disconnect()
            return True
        except Exception:
            return False

    def redeploy(self, local_paths: list) -> bool:
        """起動したいときこれ呼び出し"""
        self.remote_stop()
        if not self.remote_send(local_paths):
            return False
        return self.remote_run()

if __name__ == "__main__":
    d = Dist(ip_address="172.31.254.5", ssh_username="vyos", ssh_password="vyos", nos="vyos")
    print(d.detect_capability())
    #print(d.redeploy(local_paths=[
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/gnmi-proxy-amd64",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/mapping.yaml",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/server_config.yaml"
    #    ]
    #))
    #print(d.remote_stop())
    #print(d.remote_del(local_paths=[
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/gnmi-proxy-amd64",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/mapping.yaml",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/server_config.yaml"
    #    ]
    #))
