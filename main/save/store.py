import json
from datetime import datetime
from threading import Lock

class Store:
    def __init__(self, debug=False):
        self.log_lock = Lock()
        self.log_file = "orchestrator.log"
        self.debug = debug

    def save_log(self, ip: str, message: str):
        if not self.debug:
            log_entry = {
                "timestamp": datetime.utcnow().isoformat() + "Z",
                "device_ip": ip,
                "message": message
            }
            with self.log_lock:  # マルチスレッドからの安全性を確保
                with open(self.log_file, "a") as f:
                    f.write(json.dumps(log_entry) + "\n")
        if self.debug:
            print(f"{ip}: {message}")
