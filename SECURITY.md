# Security Policy

NewPress 1.0.0-beta.1 is a prerelease, not a stable production release. File storage is single-writer. Login sessions are persisted in private files or PostgreSQL; the complete application retains single-instance deployment boundaries. See [session persistence and recovery](SESSION_PERSISTENCE.md), including clearing sessions after a database restore. Review the documented deployment boundaries before testing with real data.

## Reporting a vulnerability

Contact **ChunYang** privately at **[Chunnel@linux.do](mailto:Chunnel@linux.do)**. Do not open a public issue for a suspected vulnerability. The canonical repository is [chunyangnet/NewPress on GitHub](https://github.com/chunyangnet/NewPress); the domestic mirror is [chunyangnet_0/newpress on Gitee](https://gitee.com/chunyangnet_0/newpress). If private vulnerability reporting is enabled on GitHub, its private report form is also an option; email remains available when that feature is not enabled.

Include the affected version/commit, deployment topology, reproduction steps, expected impact, privileges required, and relevant logs with secrets and personal data removed. Do not send production passwords, access tokens or private keys. No fixed response-time commitment is published yet.

## Scope

Security-sensitive components include package extraction, theme rendering, HTML sanitization, WASM runtime/Host API, authentication/authorization, media parsing, migrations, secrets, Docker configuration and update/rollback flows.

The project does not accept reports whose only claim is that an administrator can alter their own development files, or that WordPress PHP plugins cannot run; those are intended boundaries. Sandbox escapes, capability bypasses, XSS, SSRF, auth bypass, package traversal, signature bypass and data loss are in scope.

## Supported versions

No stable version is supported yet. The planned public prerelease is 1.0.0-beta.1; subsequent beta builds may require migration or deployment changes described in their release notes. Stable support branches and end-of-support dates will be published separately.

Security changes should include regression coverage for the affected authentication, authorization, input validation and data-isolation boundaries in the corresponding packages.

## Request and screenshot boundaries

Credential-free admin previews require a reliably resolved loopback client and
a loopback Host or the configured public Host. Forwarded host headers do not
grant access. Configure trusted proxy networks narrowly and preserve the
original Host at the reverse proxy.

Browser writes reject duplicate or malformed Origin/Fetch Metadata headers and
check Referer when Origin is absent. Public unauthenticated writes require an
explicit same-origin source; authenticated writes additionally require their
session CSRF token. Admin login and logout accept only bounded UTF-8 form bodies,
never credentials or CSRF tokens in query parameters.

The optional screenshot worker (protocol version 6) routes each browser context
through its own bounded proxy. HTTP and HTTPS CONNECT dial only the numeric IPs
approved by the request policy, including redirects and subresources. Public
requests cannot select local/reserved addresses. Local resources in explicit
local previews are confined to the selected origin and its initially approved addresses. Metadata
endpoints and IPv6 transition ranges are denied; WebSockets, direct WebRTC UDP,
and QUIC are disabled. Live previews expire after ten minutes. Chromium's sandbox
is explicitly enabled and TLS certificate verification remains enabled. Run the
worker as an unprivileged user in an environment that supports the browser sandbox.
Deploy the updated core and worker together;
the core refuses older workers. Keep the worker bound to loopback.

Each screenshot proxy permits at most 32 in-flight operations, including pending
DNS resolution and dialing; HTTP pipelining cannot bypass this budget. HTTP
requests have a 30-second absolute lifetime and CONNECT tunnels a ten-minute
absolute lifetime, in addition to idle timeouts and byte limits. Disconnects
cancel pending dials and completed requests release their operation slots.

The shared public outbound client validates every request before transport:
HTTPS on port 443, no URL credentials or fragments, no Host override, and public
IP addresses only. IPv6 addresses must be in the allowed global-unicast space
and outside excluded special-purpose ranges. Responses have a 64 KiB header
budget. Administrative exceptions use a separate explicitly approved endpoint
client. The GitHub profile/avatar fetcher uses the public client too.

Regression checks include `node --test tests/frontend/device-mockup-network.test.cjs`
and `npm run test:security:browser` (requires the project's Playwright Chromium).
The browser integration test uses isolated local fixtures and does not contact
production services.
