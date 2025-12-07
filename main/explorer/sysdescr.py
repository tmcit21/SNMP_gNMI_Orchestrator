from puresnmp import Client, V2C, ObjectIdentifier as OID
import asyncio
import re
from netmiko.snmp_autodetect import *
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



class Sysdescr():
    def __init__(self, ip_address, community):
        self.ip_address = ip_address
        self.community = community

    def get(self) -> str | None:
        async def _check():
            TIMEOUT = 2.0
            target_oid = "1.3.6.1.2.1.1.1.0"
            client = Client(self.ip_address, V2C(self.community))
            result = await asyncio.wait_for(
                client.get(OID(target_oid)),
                timeout=TIMEOUT
            )
            return result
        try:
            result = asyncio.run(_check())
            return result
        except Exception as e:
            return None

    def parse_sysdescr(self, sys_descr_raw) -> str:
        if not sys_descr_raw:
            return None
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
                """
                # Used cache data if we already queryied this OID
                if self._response_cache.get(oid):
                    snmp_response = self._response_cache.get(oid)
                else:
                    snmp_response = self._get_snmp(oid)
                    self._response_cache[oid] = snmp_response
                """
                snmp_response = sys_descr_raw.value.decode('utf-8')
                assert isinstance(snmp_response, str)
                if re.search(regex, snmp_response):
                    assert isinstance(device_type, str)
                    return device_type

        return "Unknown"



if __name__ == "__main__":
    srl = Sysdescr("172.31.254.2", "public")
    vyos = Sysdescr("172.31.254.5", "public")
    srl_r = srl.get()
    vyos_r = vyos.get()
    #print(srl_r)
    #print(vyos_r)
    #print(srl.parse_sysdescr(srl_r))
    #print(vyos.parse_sysdescr(vyos_r)))
