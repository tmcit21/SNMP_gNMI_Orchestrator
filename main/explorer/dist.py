from netmiko import ConnectHandler, file_transfer
import time, tempfile, os, yaml


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
    def __init__(self, ip_address: str, ssh_username: str, ssh_password: str, nos: str, snmp_community: str, snmp_target: str = "127.0.0.1", grpc_port: int=57401,
    if_refresh_interval: int =60):
        self.ip_address = ip_address
        self.ssh_username = ssh_username
        self.ssh_password = ssh_password
        self.nos = nos
        self.snmp_community = snmp_community
        self.snmp_target = snmp_target # ここはlocalhost使わない
        self.grpc_port = grpc_port
        self.if_refresh_interval = if_refresh_interval
        self.login = {"device_type": self.nos, "host": self.ip_address, "username": self.ssh_username, "password": self.ssh_password}
        self.arch = None #amd64かarm64か
        self.path = None #Proxyのファイルの置き場所
        self._entry_cmd_cmd = None
        self._is_capable = False

    def gen_conf(self) -> str:
        config_data = {
            "refresh_interval_sec": self.if_refresh_interval,
            "if_name_oid": ".1.3.6.1.2.1.2.2.1.2",
            "snmp_target": self.snmp_target,
            "community": self.snmp_community,
            "port": self.grpc_port
        }
        fd, path = tempfile.mkstemp(suffix=".yaml", text=True)
        with os.fdopen(fd, 'w') as f:
            yaml.dump(config_data, f, default_flow_style=False)
        return path

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
                    self.arch = ARCH_MAP.get(raw_arch, raw_arch)
                    raw_path = c.send_command("pwd").strip()
                    if not raw_path.endswith("/"):
                        raw_path += "/"
                    self.path = raw_path
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


    def check_path(self) -> str: #配置するパス
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
        temp_config_path = self.gen_conf()
        upload_targets = local_paths.copy()
        upload_targets.append(temp_config_path)
        try:
            c = ConnectHandler(**self.login)
            for local_path in upload_targets:
                dest_file = local_path.rpartition('/')[2]
                if local_path == temp_config_path:
                    file_transfer(c, source_file=local_path, dest_file="server_config.yaml", file_system=file_system, direction="put", overwrite_file=True, )
                else:
                    file_transfer(c, source_file=local_path, dest_file=dest_file, file_system=file_system, direction="put", overwrite_file=True)
                    c.send_command(f"chmod +x {file_system}{dest_file}")
            c.disconnect()
            return True
        except Exception:
            try: #悪あがき
                tmp = self.login["device_type"]
                self.login["device_type"] = "linux"
                c = ConnectHandler(**self.login)
                for local_path in upload_targets:
                    dest_file = local_path.rpartition('/')[2]
                    if local_path == temp_config_path:
                        file_transfer(c, source_file=local_path, dest_file="server_config.yaml", file_system=file_system, direction="put", overwrite_file=True, )
                    else:
                        file_transfer(c, source_file=local_path, dest_file=dest_file, file_system=file_system, direction="put", overwrite_file=True)
                        c.send_command(f"chmod +x {file_system}{dest_file}")
                c.disconnect()
                self.login["device_type"] = tmp
                return True
            except Exception:
                return False
        finally:
            if os.path.exists(temp_config_path):
                os.remove(temp_config_path)

    def remote_del(self, local_paths: list) -> bool:
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
    local_paths=[
        "/home/user/SNMP_gNMI_Orchestrator/proxy/gnmi-proxy-amd64",
        "/home/user/SNMP_gNMI_Orchestrator/proxy/mapping.yaml",
        ]
    d = Dist(ip_address="172.31.254.3", ssh_username="admin", ssh_password="NokiaSrl1!", nos="linux", snmp_community="public")
    print(d.redeploy(local_paths))
    #print(d.detect_capability())
    #print(d.redeploy(local_paths=[
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/gnmi-proxy-amd64",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/mapping.yaml",
    #    ]
    #))
    #print(d.remote_stop())
    #print(d.remote_del(local_paths=[
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/gnmi-proxy-amd64",
    #    "/home/user/SNMP_gNMI_Orchestrator/proxy/mapping.yaml",
    #    ]
    #))

