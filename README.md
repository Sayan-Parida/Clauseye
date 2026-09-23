# Clauseye

Clauseye is a contract-risk analysis application. This repository is organized
as a monorepo so the backend and future frontend can evolve together.

## Repository layout

```text
Clauseye/
├── README.md
├── .gitignore
├── backend/       # Go API and container configuration
└── frontend/      # Planned; will be added later
```

## Backend

The backend is a small Go service that exposes the Clauseye analysis API. See
[`backend/README.md`](backend/README.md) for local development, configuration,
and deployment details.

The frontend will be added under `frontend/` in a future change.
