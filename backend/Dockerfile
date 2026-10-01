# Build context is the backend directory. Go 1.22 rather than 1.27 so the image
# matches the go.mod directive; note that 1.22 is past end of life and bumping
# the go directive together with this tag is a separate, deliberate change.
FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
# -trimpath and -ldflags="-s -w" keep the binary small and reproducible. CGO is
# off so the result runs on a distroless static base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o clauseye-backend .

# Distroless static: no shell, no package manager, no libc. The healthcheck is
# the binary's own -healthcheck flag for exactly this reason.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /app/clauseye-backend .
ENV PORT=8080
EXPOSE 8080
# nonroot is uid 65532.
USER nonroot:nonroot
ENTRYPOINT ["/app/clauseye-backend"]
