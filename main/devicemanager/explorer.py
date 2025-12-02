import ipaddress
import socket
from concurrent.futures import ThreadPoolExecutor, as_completed

class Explorer:
    def __init__(self, timeout=0.1, workers=100):
        self.timeout = timeout
        self.workers = workers

    def host_alive(self, ip):
        ports = [161, 57400, 22]  # SNMP → gNMI → SSH
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
                            "password": "NokiaSrl1!",
                            "community": "public"
                        })
                except:
                    pass

        return results


if __name__ == "__main__":
    exp = Explorer(workers=100)  # 100並列
    devices = exp.explore_subnet("172.31.254.0/24")
    print(devices)
    """
    [{'ip_address': '172.31.254.1', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, {'ip_address': '172.31.254.5', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, {'ip_address': '172.31.254.2', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, {'ip_address': '172.31.254.4', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, {'ip_address': '172.31.254.3', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}, {'ip_address': '172.31.254.254', 'username': 'admin', 'password': 'NokiaSrl1!', 'community': 'public'}]
    """
