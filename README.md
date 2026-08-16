# Hookfly

Hookfly receives GitLab and GitHub webhooks, records their routing history in SQLite, and asks Dokploy to deploy configured Compose resources. The Go backend exposes separate public and management listeners; the React 19 UI talks to the management API through a same-origin Nginx proxy.

## Configure Hookfly

`configs/hookfly.yaml` is an executable starter with no runtime resources. It is valid when `conf.d` is absent or empty. `configs/routing.example/` is the complete fake reference: GitLab and GitHub sources, one Dokploy connection, one shared `production` target, and two deploy routes.

The global file is always `hookfly.yaml`. Resource documents live only in direct, regular `conf.d/*.yaml` files. They merge as one configuration set: filenames have no precedence or ordering semantics. A candidate is rejected for duplicate IDs, unknown fields or kinds, invalid references, and equal-priority overlapping routes. The complete example uses distinct explicit route priorities.

Use protected environment injection for the mandatory nonempty credentials: `GITLAB_TOKEN`, `GITHUB_WEBHOOK_SECRET`, and `DOKPLOY_API_KEY`. Keep values out of Git, URLs, command lines, and webhook payloads. The checked-in complete example contains only `${...}` references; copy the directory outside the repository before adding real identifiers and environment values.

```sh
cp .env.example .env
docker compose --env-file .env -f deploy/compose.example.yaml config
```

Set `HOOKFLY_CONFIG_DIR` in the local `.env` to the copied directory. The default `../configs` mounts the zero-resource starter. Test the checked-in examples locally with:

```sh
go test ./internal/config ./internal/runtimecfg -count=1
```

## Configure management authentication

Management authentication defaults to Authentik OIDC. Hookfly fails before opening either listener when required OIDC configuration or provider discovery is invalid; it never falls back to anonymous access. Create a dedicated Authentik OAuth2/OpenID Provider with a confidential client, Authorization Code flow, and PKCE S256. Configure these values through protected Dokploy environment injection:

```text
HOOKFLY_AUTH_MODE=oidc
HOOKFLY_EXTERNAL_URL=https://webhook.chuandashow.com
AUTHENTIK_ISSUER=https://authentik.example.invalid/application/o/hookfly/
AUTHENTIK_CLIENT_ID=<protected value>
AUTHENTIK_CLIENT_SECRET=<protected value>
AUTHENTIK_OAUTH_SCOPES=openid,profile
HOOKFLY_AUTH_ALLOWED_GROUPS=hookfly-users
HOOKFLY_AUTH_SESSION_SECRET=<Base64-encoded 32 random bytes>
```

Set Authentik's exact redirect URI to `https://webhook.chuandashow.com/api/v1/auth/callback`. The selected scope/property mappings must expose `sub`, `preferred_username`, `name`, and a string-array `groups` claim. `HOOKFLY_AUTH_ALLOWED_GROUPS` accepts comma-, space-, or semicolon-separated case-sensitive group names and defaults to `hookfly-users`; a member of any configured group receives all current management permissions. Hookfly stores no OAuth tokens, email, or groups in its eight-hour encrypted browser session.

Generate an independent session secret with `openssl rand -base64 32`. Rotating it immediately invalidates all Hookfly sessions. Local development may explicitly set `HOOKFLY_AUTH_MODE=none`; this skips OIDC, shows `Local development` in the account control, and must not be used for a remote deployment.

The management API supports browser sessions only. Anonymous requests and requests that contain only a Bearer token receive `401`; service-account and API-token authentication are not implemented. The UI checks the session before loading management data, displays the current Authentik identity, and provides a Hookfly-local logout action. Logout clears Hookfly's cookie but does not end the Authentik SSO session.

## Configure provider webhooks

The public listener exposes only these routes:

- GitLab: `POST /hooks/gitlab`
- GitHub: `POST /hooks/github/{source_id}`

For the complete example, replace the GitHub `{source_id}` with `github-example` after substituting your own source IDs.

In GitLab, create every project webhook with the same Hookfly public URL ending in `/hooks/gitlab`, select **Push events** and **Pipeline events**, and set its secret token to the protected value referenced by that source's `token`. Hookfly uses the standard `X-Gitlab-Token` request header to select and authenticate the configured source, then resolves the repository by the payload's numeric project ID. A common single-instance setup needs no GitLab base URL. Multiple GitLab instances use separate sources with distinct tokens, and duplicate GitLab source tokens make the complete configuration invalid. Source IDs remain internal routing and history identities and do not appear in the GitLab webhook URL.

GitLab's event UUID identifies a delivery for duplicate detection; it does not select configuration. Keep every GitLab source token nonempty, unique, high entropy, and protected.

In GitHub, create a repository webhook with the Hookfly public URL ending in `/hooks/github/{source_id}`, set content type to `application/json`, select the events that your routes use from **Workflow runs**, **Pushes**, **Workflow jobs**, and **Pull requests**, and set the webhook secret to the protected value referenced by that source's `secret`. GitHub sources require a nonempty webhook secret.

When both push and pipeline webhooks are enabled, the event list projects one revision lifecycle: a push appears immediately as waiting for its pipeline, then the first matching push-triggered pipeline replaces that waiting stage and continues through its source and deployment states. Hookfly correlates this lifecycle by provider, source, repository, ref, revision, and event time. The original webhook evidence remains stored as separate immutable events.

## Configure Dokploy

Create a narrowly scoped Dokploy API key and inject it only through protected environment configuration as `DOKPLOY_API_KEY`. Do not place the key in an example, Compose file, command line, URL, or webhook payload. Configure the Dokploy base URL and the target's Compose resource ID from the confirmed target instance.

For each matched deployment route, Hookfly first reads the Compose deployment history and durably records its newest deployment ID as an exclusive cursor. It then sends one direct `POST /api/compose.deploy` with `x-api-key` and polls the configured deployment history interval and timeout. The first poll that sees exactly one post-cursor deployment binds its `deploymentId`; later polls use that immutable ID. Hookfly does not use Dokploy's mutable deployment `description` as the primary identity because Git-backed deployments may replace it with `Commit: <sha>` metadata.

Dokploy endpoint and payload schemas are version-sensitive; Hookfly documents and uses only this deployment behavior, not unverified fields from a different Dokploy installation. This direct request does not depend on Git Watch Paths. Until Hookfly binds a deployment ID, it serializes sends that resolve to the same remote Compose resource. If the stored cursor was pruned or multiple post-cursor deployments cannot be distinguished safely, Hookfly marks the outcome unknown instead of guessing or automatically repeating the POST.

Hookfly redeploys the image references already present in the Dokploy Compose configuration. It does not copy a webhook revision into Compose or environment variables, and it does not replace tags or digests. Publish or update the intended image reference before the successful provider event triggers Hookfly.

## Reload and deployment boundaries

Hookfly compiles one immutable configuration candidate from `hookfly.yaml` and its discovered `conf.d` files. Sources, routes, and targets publish together as one complete generation, so a request cannot observe a mixed old/new configuration; failed candidates preserve the current generation. Use **Reload configuration** in the management UI or `POST /api/v1/config/reload` after editing the mounted directory; the backend does not restart. Invalid candidates are not applied, existing deployments finish on the old generation, and webhooks accepted while a successful reload waits or drains are durably deferred before FIFO routing against the new generation. Editing a host `.env` does not rotate credentials through reload; recreate the backend after changing a secret.

`deploy/compose.example.yaml` binds the host `HOOKFLY_CONFIG_DIR` read-only at `/etc/hookfly`, sets `HOOKFLY_CONFIG=/etc/hookfly/hookfly.yaml`, and uses `create_host_path: false`. It does not mount a Docker socket; SQLite lives in `hookfly-data`.

The Compose example publishes no container port directly. Both services join an internal application network and the external `HOOKFLY_PROXY_NETWORK`, which defaults to Dokploy's `dokploy-network`. Traefik terminates TLS and routes `Host(HOOKFLY_ROUTE_HOST) && PathPrefix(/hooks/)` to backend `:8080` with higher priority; the host-wide router sends every other request to frontend Nginx `:8080`. Nginx serves the SPA and proxies `/api/*` to backend `:8081` over the internal network. Backend `:8081` has neither a host publication nor a Traefik router.

Set `HOOKFLY_ROUTE_HOST=webhook.chuandashow.com` and keep it aligned with `HOOKFLY_EXTERNAL_URL`. `HOOKFLY_HTTPS_ENTRYPOINT`, `HOOKFLY_CERT_RESOLVER`, router priorities, and `HOOKFLY_PROXY_NETWORK` are deployment inputs. Use either the repository Traefik labels or Dokploy Domain resources as the routing source of truth, not both.

## Operations

Hookfly uses SQLite in WAL mode. For an online backup, use SQLite's backup API or the `sqlite3` `.backup` command against the mounted database so the backup is a consistent snapshot. For an offline backup, stop Hookfly cleanly before copying the database together with any `-wal` and `-shm` files or snapshotting the whole volume. Never copy only the live main database file.

For v1 recovery: stop Hookfly, back up the database, remove the old database and its `-wal`/`-shm` sidecars, then restart to create v2. Hookfly never performs those deletions automatically.

Keep backups outside the application volume, define an independent retention schedule, and test restores regularly. Restore only while Hookfly is stopped; restore a consistent backup set, start the intended version, and verify `/api/v1/health` plus representative history. Embedded migrations run at startup, so back up before upgrading and do not assume a database migrated by a newer binary can be opened safely by an older one. The YAML history limits retain terminal event history; they are not backup retention.

Retry and redeploy are explicit management actions, not automatic POST retries. Hookfly uses compare-and-swap on the current attempt, preserves earlier attempts for audit, and sends a new deployment only for the newly created attempt. If a send may have reached Dokploy or a deployment result is unknown, startup recovery first reconciles against deployment history using the persisted preflight cursor, then continues by bound deployment ID. Legacy attempts without a cursor retain exact attempt-token matching. Recovery never repeats an interrupted deployment POST automatically. Operators should inspect the recorded request/result and Dokploy deployment state before choosing retry or redeploy.

Repository navigation reflects only the current configuration. Removing a configured repository removes it from navigation after reload, but retained historical events remain under **All** until the new generation's retention limits prune them. A historical delivery whose target changed or disappeared is marked as blocked and offers no retry or redeploy action.

## Development and images

Run the same primary checks as CI:

```sh
go test ./...
go vet ./...
npm --prefix web ci
npm --prefix web audit
npm --prefix web run typecheck
npm --prefix web test -- --run
npm --prefix web run build
scripts/check-repository-test.sh
scripts/check-repository.sh
ruby scripts/check-container-workflow.rb
docker build -f Dockerfile.backend -t hookfly-backend:test .
docker build -f web/Dockerfile -t hookfly-frontend:test web
```

Pushes and manual runs first scan the current tracked tree and complete Git history for secrets with read-only repository permissions. After that gate passes, CI runs repository policy, Go, frontend, and separate backend/frontend image verification. A push to `main` builds the two release images in parallel for native `linux/amd64`. A manual **Run workflow** invocation on `main` uses native `ubuntu-24.04-arm` runners to build `linux/arm64`; it does not use QEMU.

Each release image is built once into a Docker archive. A focused Trivy gate scans that sole archive for every secret, fixable HIGH and CRITICAL vulnerabilities, misconfigurations, and image-configuration issues; each scanner runs once and no alternate image is built or scanned. CI then checksums the archive, retains the archive and checksum for one day without recompression, and publishes those same bytes only after all verification jobs pass. Registry-write jobs do not check out or execute repository source. Provenance is generated after publication against the resulting registry digest, without rebuilding the image.

Automatic amd64 publication preserves the tags `<full-commit-SHA>` and `latest`. Manual ARM publication uses `<full-commit-SHA>-arm64` and `latest-arm64`. Both run-specific candidate images must publish successfully before either image receives final tags. Each architecture-specific SHA tag is created even when the current-main API is unavailable or the run has become stale; in those cases CI leaves the floating tag unchanged. Registry updates for backend and frontend are independent, so a narrow partial-promotion window remains while the two final-tag jobs run in parallel.

The architecture-specific full commit SHA tag identifies one source revision; `latest` and `latest-arm64` are mutable convenience tags and may point to different content over time. Prefer a verified architecture-specific full SHA tag or image digest for reproducible deployments. For an ARM deployment, set `HOOKFLY_BACKEND_IMAGE` and `HOOKFLY_FRONTEND_IMAGE` to matching `-arm64` tags before rendering `deploy/compose.example.yaml`.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development and pull request workflow. Report suspected vulnerabilities through the private process in [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).
