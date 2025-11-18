import ipaddress
import socket
from concurrent.futures import ThreadPoolExecutor, as_completed

class Explorer:
    def __init__(self, timeout=0.2, workers=100):
        self.timeout = timeout
        self.workers = workers

    def host_alive(self, ip):
        ports = [22, 57400, 161]
        for p in ports:
            try:
                socket.create_connection((ip, p), timeout=self.timeout)
                return True
            except:
                continue
        return False

    def explore_subnet(self, subnet: str):
        net = ipaddress.ip_network(subnet, strict=False)
        ips = [str(ip) for ip in net.hosts()]

        results = []

        # --- 並列実行 ---
        with ThreadPoolExecutor(max_workers=self.workers) as executor:
            futures = {executor.submit(self.host_alive, ip): ip for ip in ips}

            for future in as_completed(futures):
                ip = futures[future]
                try:
                    alive = future.result()
                    if alive:
                        results.append({
                            "ip_address": ip,
                            "username": "admin",
                            "password": "admin",
                            "community": "public"
                        })
                except:
                    pass

        return results


if __name__ == "__main__":
    exp = Explorer(workers=100)  # 100並列
    devices = exp.explore_subnet("172.31.254.0/24")
    print(devices)
