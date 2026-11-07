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

Betas are not taken from the profile; their composition stays in code. Captured 2.1.318 betas differ from the gateway's per model and are tracked in #147.

## Compatibility table (#153, not enforced yet)

Besides the identity, the profile carries a table of what each captured Claude Code version really sends. It is validated at startup (unknown or duplicate keys, malformed rows, no rows for `cli_version`, files over 256 KB all stop the server). **No request is checked against it yet**: requests are forwarded exactly as before, and the startup log says `compatibility_enforced=false`.

### Schema versions

| `schema_version` | Content | Status |
|---|---|---|
| 1 | identity only | loads; logs a warning |
| 2 | identity + one beta list per version/endpoint/entrypoint/model | loads, inactive (legacy) |
| 3 | identity + exact captured request variants | shipped, inactive |

When enforcement ships it will require a complete schema 3 file; a schema 1 or 2 file will then fail at startup with migration guidance.

### Schema 3 table

```json
"compatibility": {
  "versions": [{
    "version": "2.1.318",
    "rows": [{
      "endpoint": "messages",
      "entrypoint": "cli",
      "models": ["claude-opus-4-7"],
      "variants": [{
        "selector": {"cache": "ttl1h", "thinking": "adaptive/updates", "context_management": true,
                     "effort": true, "output_format": false, "deferred_tools": true,
                     "safeguards": true, "tools": true},
        "beta_sets": [["claude-code-20250219", "oauth-2025-04-20", "..."]]
      }]
    }]
  }]
}
```

- A row is one exact version, endpoint (`messages` | `count_tokens`), entrypoint (`cli` | `sdk`) and model. There is no nearest-version, model-family or alias fallback.
- A variant is one structural request case. All eight selector fields are required; none is a wildcard.
  - `cache`: `ttl1h` when any cache_control has `ttl: "1h"`, `default` when cache_control appears without it, otherwise `none`.
  - `thinking`: `type/display` (`adaptive/updates`, `enabled/omitted`, …), a bare type when there is no display, or `absent`.
  - The rest are booleans: `context_management`, top-level `output_config.effort` / `output_config.format`, at least one tool with `defer_loading: true`, `safeguards`, and a non-empty `tools` array.
- `beta_sets` lists every exact beta list captured for that case, in wire order. One case can have more than one list: 2.1.309 sends its title request with and without `dangerous-tool-use`.
- The target version is `cli_version`. Upgrading Claude Code means capturing the new version and adding its rows. A `cli_version` without rows is rejected at startup.

### Migrating a custom schema 2 file

1. Set `schema_version` to 3.
2. Replace each row's `default_betas` / `optional_betas` / `requires` with `variants`. Copy only lists you captured from the real client, never a union or a subset of them.
3. Remove `body_contract`.
4. `aliases` must be absent or empty. List each dated model id in the `models` of the rows where it was actually captured.

### Shipped content

- Versions 2.1.280, 2.1.309 and 2.1.318: 41 rows, 104 variants.
- They come from offline captures of ordinary sessions, compact, thinking-off, title-generation and 1M runs. The captures used synthetic credentials with networking disabled, and only sanitized metadata is kept in the repository.
- 2.1.280 rows exist as sources only:
  - Its `count_tokens` requests map onto 2.1.318.
  - Its `messages` requests carry `prompt-caching-scope-2026-01-05`, which 2.1.318 never sends, so they are incompatible.
- Dated model ids (`claude-sonnet-4-5-20250929`, `claude-haiku-4-5-20251001`) are captured but not shipped, because dated ids are not offered to customers by default. An operator who needs one adds it as an exact model in the rows where it was captured.

### What the table does not cover (unsupported once enforced)

Requests outside the captured grammar would be rejected locally with `unsupported_client_combination`:
- documents/PDF;
- images placed directly in a user message (images inside tool results are covered);
- `redacted_thinking` blocks;
- `tool_choice`, `stop_sequences`, `top_p`, `top_k`;
- server tools (a tool with `type`);
- cache_control on tools, or a TTL other than 1h;
- other `context_management` edits;
- effort levels outside the model's catalog.

2.1.318 interactive mode never sent a title-generation request offline. A 2.1.309 interactive title request therefore has no target, and once enforced those users would get no automatic session title. SDK title requests for sonnet-4-6 and haiku-4-5 are covered.

## Changing the profile

A new profile changes every covered account at once when the process restarts. There is no gradual rollout yet (#145). Roll back by deploying the previous release with its matching profile.

To produce a profile, capture the official CLI build for the chosen OS/arch in an isolated container: `docker run --network none`, a synthetic `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_BASE_URL` pointing at a local receiver inside the container, telemetry and auto-update disabled. Capture an interactive session (entrypoint `cli`), not only `-p` (`sdk-cli`). Store the redacted evidence under `backend/internal/pkg/claude/testdata/client_profile_capture/`, put its SHA-256 in `source.evidence_sha256`, and update the built-in copy in `internal/pkg/claude/client_profile.go`. Tests check that the file, the built-in copy and the evidence agree. The capture uses a custom base URL, so it does not prove upstream acceptance. In 2.1.318 the CLI did not send `x-client-request-id` to the custom URL; the gateway keeps adding it for mimicked requests.

## Header evidence log

With `gateway.log_claude_headers` (default on), the gateway writes `claude.headers` events:

- `direction=inbound`: the client's request headers, once per request, the first time an Anthropic account request is prepared for it.
- `direction=outbound`, `snapshot=prepared`: every attempt's headers at the upstream send, with `operation`, `account_id`, `attempt` and `profile_revision`. Headers added by the transport itself (Host, Content-Length, connection headers) are not in it.

Both carry `client_request_id`; background calls (account tests, models, reset credits) carry `operation_id` and have no inbound line. Bodies are never logged. Values are bounded (2 KB each, 128 headers, 32 KB total) and truncation is flagged.

Credential headers are `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `x-api-key` and names containing token, secret, password, passwd, credential or auth, or ending in `-key`. Only recognised token formats (`sk-…`, which covers Anthropic OAuth/API keys and the gateway's default keys, at least 24 characters) show the last 8 characters, the length and the first 12 hex of SHA-256. Every other value, including passwords and unknown formats, shows only its length. Only `Bearer` and `Basic` are recognised as schemes, and only on the Authorization headers; Basic credentials show their length, and any other scheme is redacted whole without echoing it. Cookies keep their names and each value follows the same rule; a malformed cookie header and `Set-Cookie` are redacted whole. The env-gated debug dump (`SUB2API_DEBUG_GATEWAY_BODY`) uses the same rule.
