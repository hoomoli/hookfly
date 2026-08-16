# Security Policy

## Reporting a vulnerability

Please use [GitHub private vulnerability reporting](https://github.com/hoomoli/hookfly/security/advisories/new) for suspected vulnerabilities. Include the affected version or commit, a minimal reproduction, the impact, and any suggested mitigation.

Do not include credentials, webhook payloads, internal URLs, database contents, or exploit details in a public issue. If private reporting is unavailable, open a minimal public issue asking the maintainer to establish a private contact channel, without disclosing sensitive details.

## Supported versions

Hookfly does not publish a fixed long-term support schedule or a supported-version matrix. Include the affected commit, image tag, or image digest in each report.

## Deployment responsibility

The public webhook listener and management listener are separate trust boundaries. `/hooks/*` remains public but requires each provider's configured webhook secret. Management routes use an encrypted Hookfly browser session established through Authentik OIDC; anonymous requests and Bearer-only requests are not authorized.

Keep backend management port `8081` private to the application network. Route `/hooks/*` to backend public port `8080`, route all other traffic to frontend Nginx, and let Nginx proxy `/api/*` internally to `8081`. Terminate TLS at the trusted reverse proxy and do not configure a second direct management router or host publication.

Keep the Authentik client secret and Hookfly session secret in protected runtime configuration. They must be independent. Rotating the session secret invalidates every current Hookfly session; rotating the Authentik client secret requires updating both systems before new logins work. Removing a user from an allowed group takes effect when the existing fixed eight-hour Hookfly session expires, unless the session secret is rotated earlier.

Rotate any exposed Authentik client secret, Hookfly session secret, GitLab token, GitHub webhook secret, or Dokploy API key and review stored event history after a suspected compromise. Hookfly logout clears only its local cookie and does not terminate the user's Authentik SSO session.
