import json
import threading
import time
import urllib.request

import uvicorn

from laya_adapter import app


config = uvicorn.Config(app, host="127.0.0.1", port=8000, log_level="warning")
server = uvicorn.Server(config)
thread = threading.Thread(target=server.run, daemon=True)
thread.start()
try:
    for _ in range(120):
        try:
            with urllib.request.urlopen("http://127.0.0.1:8000/health", timeout=1):
                break
        except Exception:
            time.sleep(1)
    else:
        raise RuntimeError("adapter did not start")

    payload = {
        "states": [
            "The party shall indemnify the client against all claims without limitation.",
            "This agreement automatically renews for successive one-year terms unless terminated.",
            "The parties shall comply with the governing law and jurisdiction stated herein.",
        ]
    }
    request = urllib.request.Request(
        "http://127.0.0.1:8000/analyze-batch",
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=120) as response:
        body = json.load(response)
    results = body.get("results")
    assert isinstance(results, list), body
    assert len(results) == 3, body
    assert all(isinstance(item.get("answers"), dict) for item in results), body
    print(json.dumps(body, indent=2))
    print("3-clause predict_batch smoke test passed")
finally:
    server.should_exit = True
    thread.join(timeout=10)
