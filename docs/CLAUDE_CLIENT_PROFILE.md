# Claude client profile

Since #144 the gateway's Claude Code identity comes from one reviewed file, loaded once at startup:
`backend/resources/claude-client-profile/profile.json` (config key `gateway.claude_client_profile_file`, default `./resources/claude-client-profile/profile.json`). Docker images ship it under `/app/resources`. Release archives carry it at `resources/claude-client-profile/profile.json`; `deploy/install.sh` (install, upgrade, version install) and the in-app update install it under `/opt/sub2api/resources/...`, replacing the shipped copy. `install.sh` keeps the previous copy as `profile.json.backup` when it changes. The in-app update refreshes `profile.json.backup` on every update to the profile the previous binary ran with, so its rollback restores the matching binary and profile. A profile at an operator-configured path is never replaced.

The profile holds the CLI version, `User-Agent`, `anthropic-version` and the `X-Stainless-*` values (package version, runtime and version, OS, arch, timeout), plus `x-app`, `anthropic-dangerous-direct-browser-access` and source metadata. Loading is strict: unknown or duplicate keys, missing fields, a User-Agent that does not match the version, control characters or a bad digest all stop the server. There is no fallback to older or built-in values: a process that has not loaded a profile fails instead of sending some other identity.

## Upgrading from a version before #144

Binary installs (not Docker) need the profile before the new binary starts. An in-app update started from a version before #144 runs the old updater, which installs only the binary. For that first upgrade, do one of:

- upgrade with `deploy/install.sh upgrade` (installs `resources/claude-client-profile/profile.json` with the binary), or
- before restarting the new binary, copy `resources/claude-client-profile/profile.json` from the release archive to `/opt/sub2api/resources/claude-client-profile/profile.json` (or the path set in `gateway.claude_client_profile_file`).

Otherwise the new binary refuses to start and says which file is missing. Later in-app updates install the profile themselves. Docker images already contain it.

## What uses it

Every outbound request on an Anthropic OAuth account (mimicked or native Claude Code, messages and count_tokens, default or custom base URL), plus account tests, model listing and reset-credit calls, has every profile-owned header replaced by the profile's value, in any casing: `User-Agent`, `anthropic-version`, all `X-Stainless-*` except the retry count, `x-app`, `anthropic-dangerous-direct-browser-access`. This does not depend on fingerprint unification or on the fingerprint cache being reachable. The body's `cc_version` uses the profile version, so header and body always agree. Account fingerprints keep their ClientID. Their software fields are aligned to the profile and are never learned from client headers. API-key, relay, Bedrock, Vertex and other platforms are unchanged.

Fingerprint unification still controls the metadata/ClientID rewriting; it no longer lets native requests keep the client's software headers.

The old mechanisms no longer apply: the hourly GitHub release sync is removed, the `claude_code_client_version` and `claude_code_version_auto_sync_enabled` settings are read-only (an unchanged value is accepted, a change returns 400), and `SUB2API_CLAUDE_CLI_VERSION` is ignored with a startup warning. The admin page shows the active profile read-only.

The `cc_version` suffix is computed from the first user text that is not a client-injected `<system-reminder>`, matching captured 2.1.318 traffic. Session ids still derive from the first text block, so existing sessions keep their ids.

Betas are not part of the profile; their composition stays in code. Captured 2.1.318 betas differ from the gateway's per model and are tracked in #147.

## Changing the profile

A new profile changes every covered account at once when the process restarts. There is no gradual rollout yet (#145). Roll back by deploying the previous release with its matching profile.

To produce a profile, capture the official CLI build for the chosen OS/arch in an isolated container: `docker run --network none`, a synthetic `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_BASE_URL` pointing at a local receiver inside the container, telemetry and auto-update disabled. Capture an interactive session (entrypoint `cli`), not only `-p` (`sdk-cli`). Store the redacted evidence under `backend/internal/pkg/claude/testdata/client_profile_capture/`, put its SHA-256 in `source.evidence_sha256`, and update the built-in copy in `internal/pkg/claude/client_profile.go`. Tests check that the file, the built-in copy and the evidence agree. The capture uses a custom base URL, so it does not prove upstream acceptance. In 2.1.318 the CLI did not send `x-client-request-id` to the custom URL; the gateway keeps adding it for mimicked requests.

## Header evidence log

With `gateway.log_claude_headers` (default on), the gateway writes `claude.headers` events:

- `direction=inbound`: the client's request headers, once per request, the first time an Anthropic account request is prepared for it.
- `direction=outbound`, `snapshot=prepared`: every attempt's headers at the upstream send, with `operation`, `account_id`, `attempt` and `profile_revision`. Headers added by the transport itself (Host, Content-Length, connection headers) are not in it.

Both carry `client_request_id`; background calls (account tests, models, reset credits) carry `operation_id` and have no inbound line. Bodies are never logged. Values are bounded (2 KB each, 128 headers, 32 KB total) and truncation is flagged.

Credential headers are `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `x-api-key` and names containing token, secret, password, passwd, credential or auth, or ending in `-key`. Only recognised token formats (`sk-…`, which covers Anthropic OAuth/API keys and the gateway's default keys, at least 24 characters) show the last 8 characters, the length and the first 12 hex of SHA-256. Every other value, including passwords and unknown formats, shows only its length. Only `Bearer` and `Basic` are recognised as schemes, and only on the Authorization headers; Basic credentials show their length, and any other scheme is redacted whole without echoing it. Cookies keep their names and each value follows the same rule; a malformed cookie header and `Set-Cookie` are redacted whole. The env-gated debug dump (`SUB2API_DEBUG_GATEWAY_BODY`) uses the same rule.
