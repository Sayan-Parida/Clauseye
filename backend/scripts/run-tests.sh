#!/bin/sh
# Go test suite, run inside golang:1.22-alpine.
#
# Kept as a file rather than an inline `sh -c` string because PowerShell mangles
# quotes and metacharacters when passing long arguments to native executables.
#
# Invoked by scripts/run-tests.ps1, which mounts this repository at /src and the
# host module cache at /gomod. See that script for why the suite is not run
# directly on the host.

set -eu

echo "gofmt..."
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
    echo "gofmt required for:"
    echo "$unformatted"
    exit 1
fi

echo "go vet..."
go vet ./...

echo "go test..."
go test -count=1 ./... 2>&1

if [ "${COVERAGE:-0}" = "1" ]; then
    echo "coverage..."
    go test -count=1 -coverprofile=/tmp/cover.out ./... >/dev/null
    go tool cover -func=/tmp/cover.out | tail -1
    go tool cover -func=/tmp/cover.out | sort -k3 -n | head -20
fi
