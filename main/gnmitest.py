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
        path=["/lldp/state/chassis-id"],
        encoding="json_ietf"
    )

    result = result#["notification"][0]["update"][0]["val"]["interface"]
    print(result)
