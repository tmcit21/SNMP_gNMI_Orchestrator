from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import asyncio
from puresnmp import Client, V2C, PyWrapper
import uvicorn

app = FastAPI()

class PollRequest(BaseModel):
    ip: str
    oid: str
    community: str = "public"

@app.post("/poll")
async def snmp_poll(req: PollRequest):
    """HTTPリクエストを受け取り、SNMPポーリングを非同期で実行して結果を返す関数"""
    target_ip = req.ip
    target_oid = req.oid
    community = req.community

    try:
        client = PyWrapper(Client(target_ip, V2C(community)))
        output = await client.get(target_oid)
        if isinstance(output, bytes):
            output = output.decode("utf-8", errors="replace")
        output = str(output)
        print(f"要求した値={target_oid}\n取得した値={output}")
        return {"status": "OK", "value": output}
    except Exception as e:
        error_message = str(e)
        raise HTTPException(
            status_code=500,
            detail={"status": "Error", "value": error_message}
        )

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=50051)
