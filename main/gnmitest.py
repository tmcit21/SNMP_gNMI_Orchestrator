import asyncio
from pygnmi.client import gNMIclient

with gNMIclient(
    target=("172.31.254.2", 57401),
    username="admin",
    password="NokiaSrl1!",
    insecure=True,
    skip_verify=True,
) as gc:

    result = gc.get(
        path=["/lldp/interfaces/interface/neighbors/"],
        encoding="json_ietf"
    )

    print(result)
