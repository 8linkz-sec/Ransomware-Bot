$ErrorActionPreference = "Stop"

if (-not (Get-Command golangci-lint -ErrorAction SilentlyContinue)) {
    throw "golangci-lint is not installed. Install it from https://golangci-lint.run/usage/install/ and rerun scripts/lint.ps1."
}

golangci-lint run ./...
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
