# Clauseye Laya Adapter POC

This is a standalone proof-of-concept inference service. It does not modify or integrate with the Go backend.

The adapter uses the official Laya Python SDK and `Router.predict_batch()` and exposes our own internal endpoint:

- `GET /health`
- `POST /analyze-batch`

It does not implement or modify Laya's official `/v1/systemone` protocol.

## Local setup

```powershell
cd C:\Users\Sayan\Clauseye\backend
py -3.11 -m venv .venv-laya
.venv-laya\Scripts\python.exe -m pip install --upgrade pip
.venv-laya\Scripts\python.exe -m pip install -r laya_requirements.txt
.venv-laya\Scripts\python.exe -m uvicorn laya_adapter:app --host 127.0.0.1 --port 8000
```

In another terminal:

```powershell
cd C:\Users\Sayan\Clauseye\backend
.venv-laya\Scripts\python.exe test_laya_adapter.py
```

The first startup downloads the configured checkpoint from Hugging Face unless it is already cached. The official Router checkpoint names are used; do not pass the full Hugging Face repository ID as `LAYA_MODEL`.

## Configuration

- `LAYA_MODEL`: Router checkpoint name; default `english`; supported official names are `english`, `multilingual`, and `typed-decisions`
- `LAYA_MODEL_PATH`: optional local checkpoint path mapped to `LAYA_MODEL`
- `LAYA_DEVICE`: default `cpu`; use `cuda` only with a compatible PyTorch/CUDA install
- `LAYA_BATCH_SIZE`: default `50`
- `LAYA_MAX_STATES`: default `50`
- `HF_HOME`: model cache location

The service uses nine structured `noul` questions for the current Clauseye taxonomy and returns Laya's typed answers without generated explanations.

## Docker

```powershell
docker compose -f docker-compose.laya.yml build
docker compose -f docker-compose.laya.yml up
```

The default image uses CPU PyTorch. A GPU image requires selecting a CUDA-compatible PyTorch installation and a CUDA-capable host; that is intentionally not configured here.
