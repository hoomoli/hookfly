## Summary

Describe the user-visible or maintenance outcome.

## Safety

Explain effects on routing, persistence, deployment requests, credentials, APIs, and upgrades. Write `None` when the change has no such effect.

## Verification

- [ ] Tests were added or updated for behavior changes.
- [ ] `go test ./...` and `go vet ./...` pass when Go code changed.
- [ ] Frontend tests, typecheck, and build pass when web code changed.
- [ ] Documentation and both UI locales are current.
- [ ] `scripts/check-repository.sh` passes.
- [ ] The change contains no credentials, private data, local configuration, or generated output.
