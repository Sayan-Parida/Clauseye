# Clauseye backend

Small Go proxy for Clauseye's contract-risk analysis. It has no database or
authentication; requests are rate-limited in memory to 10 per IP per minute.

## Run locally

PowerShell:

```powershell
$env:AI_GATEWAY_API_KEY = "your-key"
$env:CORS_ORIGIN = "http://localhost:3000" # optional; defaults to *
go run .
```

`POST /analyze` accepts `{"clauses":["..."]}` and `GET /health` returns
`{"status":"ok"}`. Each upstream request has a ten-second timeout and one retry.

The backend sends evaluations to the AI Gateway at:

```text
https://ai-gateway.vercel.sh/v1/evaluate
```

The API key is provided through the `AI_GATEWAY_API_KEY` environment variable.
Do not commit API keys or environment files.

## Deployment

The backend is deployed as a Linux container on Azure App Service:

- App Service: `clauseye-backend-app`
- Resource group: `clauseye-rg`
- Container image: `clauseyeacr.azurecr.io/clauseye-backend:latest`

The service requires `AI_GATEWAY_API_KEY` to be configured in its App Service
settings. It listens on port `8080` and provides `/health` for health checks.
