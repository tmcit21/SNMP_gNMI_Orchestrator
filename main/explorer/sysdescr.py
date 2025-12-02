from puresnmp import Client, V2C, ObjectIdentifier as OID
import asyncio
import re

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

    def parse_sysdescr(self, sys_descr_raw) -> dict:
        if sys_descr_raw == None:
            return None
        """
        {'vendor': 'Vendor', 'nos': 'NOS', 'raw': "OctetString(b'raw_data')"}
        """
        sys_descr = sys_descr_raw
        if isinstance(sys_descr_raw, bytes):
            try:
                sys_descr = sys_descr_raw.decode('utf-8', errors='ignore')
            except:
                sys_descr = str(sys_descr_raw)
        sys_descr = str(sys_descr)

        #NOS判定 (上から順にマッチ)
        signatures = [
            # --- Containerlab ---
            (r"SRLinux", "Nokia", "srlinux"),
            (r"VyOS", "VyOS", "vyos"),
            (r"CEOSLab", "Arista", "ceos"),
            (r"XRv", "Cisco", "ios_xrv"),
            (r"SONiC", "Community", "sonic"),

            # --- other ---
            (r"Cisco IOS XR", "Cisco", "ios_xr"),
            (r"Cisco IOS Software", "Cisco", "ios"),
            (r"Cisco NX-OS", "Cisco", "nx_os"),
            (r"Arista Networks EOS", "Arista", "eos"),
            (r"JUNOS", "Juniper", "junos"),
            (r"FortiGate", "Fortinet", "fortios"),

            # --- Fallback ---
            (r"Linux", "Generic", "linux"),
        ]

        for pattern, vendor, nos in signatures:
            if re.search(pattern, sys_descr, re.IGNORECASE):
                return {"vendor": vendor, "nos": nos, "raw": sys_descr}

        return {"vendor": "Unknown", "nos": "Unknown", "raw": sys_descr}



if __name__ == "__main__":
    srl = Sysdescr("172.31.254.2", "public")
    vyos = Sysdescr("172.31.254.5", "public")
    srl_r = srl.get()
    vyos_r = vyos.get()
    print(srl.parse_sysdescr(srl_r))
    print(vyos.parse_sysdescr(vyos_r))
