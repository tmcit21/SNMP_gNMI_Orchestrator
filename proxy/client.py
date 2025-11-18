# client.py
import requests
import json

# サーバーのアドレス
SERVER_URL = "http://localhost:50051/poll"

# 送信するデータ（JSON形式）
payload = {
    "ip": "172.20.20.2",
    "oid": "1.3.6.1.2.1.1.5.0",
    "community": "public"
}

print("--- SNMP PollリクエストをHTTPで送信します ---")

try:
    response = requests.post(SERVER_URL, json=payload, timeout=10)
    response.raise_for_status()
    print(json.dumps(response.json(), indent=2, ensure_ascii=False))

except requests.exceptions.RequestException as e:
    print(e)
