# Long context admission

The beta policy rule for `context-1m-2025-08-07` (Admin → Settings → Beta policy) is the gateway's long context switch. There is no separate setting.

| Action | Request with the 1M marker | Request without the marker |
| --- | --- | --- |
| block (default) | rejected before any upstream call | checked against the standard window |
| filter | marker removed | checked against the standard window |
| pass | forwarded as before | long context allowed (explicit opt-in) |

The rule applies only to the accounts in its scope and the models in its whitelist; anything outside is not protected. A missing rule, missing or unreadable-as-JSON settings, and the old generated default (pass for Sonnet 5, filter otherwise) all mean block for every scope. The old default is replaced when settings are read, and saving the page persists it. A customized rule is kept as it is.

## The standard window check

It runs on the exact body about to be sent to inference: after protocol conversion (Chat Completions, Responses), model mapping, OAuth mimicry and every rewritten retry. The request is admitted when input tokens + `max_tokens` + 8,192 ≤ 200,000. These numbers are a local policy, not a provider limit. Every Anthropic model the gateway routes (Claude 3 and later) has a standard window of at least 200k, so one window serves all of them.

1. If `max_tokens` alone leaves no room, it is rejected locally.
2. For plain text and local function tools, a reviewed calibration profile for the exact route and model can admit a clearly small request or reject a clearly huge one without a counter call. The profile is empirical, not exact tokenization. Highly repetitive or padded input may be rejected even though it would fit.
3. Everything else is counted once with the same account's `count_tokens` endpoint, inside a 5-second budget per client request. This adds latency and counter traffic to the account.
4. Routes without a counter (Bedrock, Vertex) reject requests that step 2 cannot decide.

Calibrated profiles exist only for `claude-sonnet-4-6` and `claude-opus-4-6` on Anthropic OAuth accounts without a custom base URL. Every other model, alias, route and content type uses step 3. The profiles were calibrated on 2026-11-05 with one OAuth account and a frozen synthetic corpus: 48 samples per model, 24 for fitting and 24 held out. The samples and a refit test are in `backend/internal/service/testdata/long_context_calibration/`. The bounds are empirical. No held-out sample broke them, but one highly compressible fixture counted below the lower bound, so such input can be rejected near the window even though it would fit.

Measured body-size bands. Bodies at or below the first number are admitted without a count. Bodies at or above the second are rejected without a count. Everything in between is counted.

| max_tokens | claude-sonnet-4-6 | claude-opus-4-6 |
| --- | --- | --- |
| 1,024 | ≤ 127,614 / ≥ 2,108,952 bytes | ≤ 127,613 / ≥ 2,107,929 bytes |
| 8,192 | ≤ 122,604 / ≥ 2,032,979 | ≤ 122,604 / ≥ 2,031,993 |
| 32,000 | ≤ 105,965 / ≥ 1,780,638 | ≤ 105,964 / ≥ 1,779,775 |
| 64,000 | ≤ 83,600 / ≥ 1,441,471 | ≤ 83,599 / ≥ 1,440,772 |

The bytes are the final prepared body, including the gateway's own additions. No production request-size distribution has been measured, so how many count calls this saves is unknown.

Only text with known fields is eligible for step 2: MCP servers, containers, server tools, citations, documents, images, thinking blocks and any field the gateway does not recognize always go to the counter. A counter response that is not one complete JSON object with a non-negative integer `input_tokens` is a 503.

## Calibration

`backend/cmd/longctx-calibrate` (build tag `calibration`, never part of a normal build) runs the bounded count-only experiment on the gateway host. It reads the approved account through a read-only database session without running migrations, and it prepares each fixture with the gateway's own Messages preparation. Each request goes through the account's own proxy and TLS profile, redirects are refused, and every request is checked before sending to make sure it goes to `api.anthropic.com` with a bearer credential. Without `-live` it only prepares and checks the requests and prints the manifest hash. With `-live` it records every attempt in `$HOME/longctx-calibration-139/ledger.jsonl` before sending it. The ledger binds the manifest and the 98-attempt budget. A stop (401, 403, 429, a redirect, two transport failures in a row, or a failed readiness check) is permanent. Nothing is ever retried. A crash that leaves an attempt without an outcome blocks further runs. `report.json` holds the fitted coefficients, held-out violations, false rejections and byte bands, or a fit error if any sample is missing.

## Client errors

No retry, account switch, account state change or Retry-After header is involved. After a keepalive has been sent, the error is a single terminal SSE event in the client's protocol.

| Status | Code | Meaning |
| --- | --- | --- |
| 400 `invalid_request_error` | `long_context_disabled` | the 1M marker was sent, or the request exceeds the standard window |
| 400 `invalid_request_error` | `long_context_count_unavailable` | this route or content cannot be counted |
| 503 `api_error` | `long_context_count_unavailable` | the counter timed out or failed; a later attempt may work |
| 503 `api_error` | `long_context_policy_unavailable` | the policy could not be read |

The public `/v1/messages/count_tokens` endpoint is unchanged. A count it returns does not grant inference.

Decisions are logged as `long_context.admission` with the decision, entry point, model, route, account ID, body size, `max_tokens`, counted tokens and budget. Prompt content and credentials are never logged.
