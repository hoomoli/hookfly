# Contributing to Hookfly

Thank you for helping improve Hookfly.

## Before opening a change

- Use an issue to discuss behavior changes that alter APIs, persistence, routing, deployment safety, or operator workflows.
- Report security issues through the process in [SECURITY.md](SECURITY.md), not a public issue.
- Keep changes focused. Do not commit credentials, real webhook payloads, internal addresses, local configuration, databases, logs, or generated output.

## Development setup

Use Go 1.25, Node.js 22, npm, and Docker with Compose support.

```sh
go test ./...
go vet ./...
npm --prefix web ci
npm --prefix web test -- --run
npm --prefix web run typecheck
npm --prefix web run build
scripts/check-repository-test.sh
scripts/check-repository.sh
```

Mount a local configuration directory containing `hookfly.yaml` and optional direct `conf.d/*.yaml` resources. `configs/hookfly.yaml` is the zero-resource starter; `configs/routing.example/` is the complete fake reference. Keep runtime configuration directories and `.env` local, and start environment values from `.env.example`.

## Configuration schema policy

Hookfly is under active development and has one current YAML configuration schema. Configuration documents use `kind` to select their structure and must not include an `api_version` field. Do not add schema-version negotiation, legacy decoders, or compatibility branches unless the project explicitly adopts a stable configuration contract.

When the configuration schema changes, update the decoder, types, checked-in examples, documentation, tests, and deployment configuration together. Unknown fields remain strict errors, so operators must deploy matching configuration and binaries as one change.

## Pull requests

1. Add or update a failing test before changing behavior.
2. Update the English public documentation and both UI locales when user-visible behavior changes.
3. Run `scripts/check-repository.sh` and the relevant commands above.
4. Use English [Conventional Commits](https://www.conventionalcommits.org/) messages.
5. Explain the user impact, safety considerations, and verification evidence in the pull request.

By contributing, you agree that your contribution is licensed under the MIT License in [LICENSE](LICENSE).
