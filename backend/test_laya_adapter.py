import json
import os
import urllib.request


url = os.getenv("LAYA_ADAPTER_URL", "http://127.0.0.1:8000/analyze-batch")
payload = {
    "states": [
        "The party shall indemnify the client against all claims without limitation.",
        "This agreement automatically renews for successive one-year terms unless terminated.",
        "The parties shall comply with the governing law and jurisdiction stated herein.",
    ]
}
request = urllib.request.Request(
    url,
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
