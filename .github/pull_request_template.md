## Summary

- 

## Verification

`go test ./...` (plain and `-race`) runs in CI on Linux for every pull
request (`.github/workflows/test.yml`); it does not need to be re-run locally
just to satisfy this checklist, though a failure should be reproduced and
diagnosed locally before pushing a fix.

- [ ] CI's `go test (Linux)` job is green, or a red result is understood and addressed.
- [ ] Focused package tests:
- [ ] `go run . --check-config --config-dir ./configs`

## Security and Operations

- [ ] No API keys, webhook URLs, private feed URLs, logs, or status files are committed.
- [ ] Config, data, retry, or delivery behavior changes are documented.
- [ ] RSS URL validation, webhook validation, retry, quiet-hours, and dead-letter behavior are preserved or explicitly described.
- [ ] Rollback or operator action is noted when needed.
