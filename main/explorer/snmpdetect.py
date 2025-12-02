import re
from netmiko.snmp_autodetect import *
#https://github.com/ktbyers/netmiko/blob/develop/netmiko/snmp_autodetect.py
# SSHDetectはまだ対応していないNOSもあるのでここに追加
# SNMP_MAPPER_BASE->SNMP_MAPPERにしたので様子見
# netmikoのSNMPはタイムアウトめちゃ待つ&タイムアウト設定がめんどいので既にSNMPレスポンスあったやつだけこのクラスのメソッド使う
# これのSNMP_MAPPERとSNMP_MAPPER_BASEだけimportしてsysdescr.pyとかpruneに組み込めばSNMP GETを1回で終わらせられる ←←←←←←これ絶対後で着手
SNMP_MAPPER["nokia_srl"] = {
        "oid": ".1.3.6.1.2.1.1.1.0",
        "expr": re.compile(r".*SR\s?Linux.*", re.IGNORECASE),
        "priority": 99,
    }
SNMP_MAPPER["vyos"] = {
        "oid": ".1.3.6.1.2.1.1.1.0",
        "expr": re.compile(r"VyOS", re.IGNORECASE),
        "priority": 99,
    }

"""
    #テンプレート
    "nos": {
        "oid": ".1.3.6.1.2.1.1.1.0",
        "expr": re.compile(r"他と被らないような正規表現", re.IGNORECASE),
        "priority": 99,
    },
"""


SNMP_MAPPER |= SNMP_MAPPER_BASE
_original = SNMPDetect.autodetect

def patched_autodetect(self):
    snmp_mapper_orig = []
    for k, v in SNMP_MAPPER.items():
        snmp_mapper_orig.append({k: v})
    snmp_mapper_list = sorted(
        snmp_mapper_orig, key=lambda x: list(x.values())[0]["priority"]  # type: ignore
    )
    snmp_mapper_list.reverse()
    for entry in snmp_mapper_list:
        for device_type, v in entry.items():
            oid: str = v["oid"]  # type: ignore
            regex: Pattern = v["expr"]

            # Used cache data if we already queryied this OID
            if self._response_cache.get(oid):
                snmp_response = self._response_cache.get(oid)
            else:
                snmp_response = self._get_snmp(oid)
                self._response_cache[oid] = snmp_response

            # See if we had a match
            assert isinstance(snmp_response, str)
            if re.search(regex, snmp_response):
                assert isinstance(device_type, str)
                return device_type

    return None

SNMPDetect.autodetect = patched_autodetect

class SNMP():
    def __init__(self, ip_address: str, community: str):
        self.ip_address = ip_address
        self.community = community
        self.auto = {"hostname": self.ip_address, "snmp_version": "v2c", "community": self.community}

    def snmpautoconn(self)-> str | None:
        guesser = SNMPDetect(**self.auto)
        return guesser.autodetect()

if __name__ == "__main__":
    device = SNMP(ip_address="172.31.254.2", community="public")
    print(device.snmpautoconn())
    device = SNMP(ip_address="172.31.254.5", community="public")
    print(device.snmpautoconn())
