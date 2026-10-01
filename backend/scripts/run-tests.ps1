# Runs the Go test suite inside a container.
#
# Why not `go test` directly on Windows:
#
# `go test` compiles a test binary and then executes it. On this machine a
# Windows Application Control / Defender policy blocks freshly compiled
# executables from user-writable locations, so `go test` fails with
#
#     fork/exec ...\clauseye-backend.test.exe: An Application Control policy
#     has blocked this file
#
# before a single test runs. Compiling into the repository instead of %TEMP%
# was blocked as well, and the verdict looks like a reputation decision rather
# than a path rule, so it cannot be reliably worked around from inside the build.
#
# Running the suite in golang:1.22-alpine matches the declared go.mod version and
# the image the Dockerfile builds from, and sidesteps the host policy entirely.
# This is also what CI would do.
#
# Usage:
#   powershell -File scripts/run-tests.ps1
#   powershell -File scripts/run-tests.ps1 -Coverage

param(
    [switch]$Coverage
)

$ErrorActionPreference = 'Stop'
$backend = Split-Path -Parent $PSScriptRoot
$modCache = Join-Path $env:USERPROFILE 'go\pkg\mod'

if (-not (Test-Path -LiteralPath $modCache)) {
    throw "Go module cache not found at $modCache"
}

$envs = @(
    '-e', 'GOMODCACHE=/gomod',
    '-e', 'GOFLAGS=-mod=mod',
    '-e', 'GOCACHE=/tmp/gocache'
)
if ($Coverage) {
    $envs += @('-e', 'COVERAGE=1')
}

& docker run --rm `
    '-v' "${backend}:/src" `
    '-v' "${modCache}:/gomod:ro" `
    '-w' '/src' `
    @envs `
    'golang:1.22-alpine' `
    'sh' '/src/scripts/run-tests.sh'

exit $LASTEXITCODE
