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

The check makes no upstream call. Admission never calls `count_tokens`.

1. If `max_tokens` alone leaves no room, the request is rejected.
2. For plain text and local function tools on a calibrated model, the profile admits a clearly small body or rejects a clearly huge one.
3. Between those two, the body is estimated at 0.4037 tokens per byte plus 8,192: the highest realistic ratio measured (0.367) plus 10%. It is rejected if the estimate exceeds the budget, otherwise admitted. With `max_tokens` 1,024 the cut-off is about 452 KB. Prose-heavy requests (about 0.19 tokens/byte) can be rejected from about 86k real tokens, a false rejection well before the window. Dense content such as emoji or random IDs (up to about 0.72) can pass above the window; the upstream credits error is still contained without account switching (#132).
4. Content, models and routes without a calibrated bound are admitted. Only step 1 and the 1M-marker rule apply to them. This covers other models and dated aliases, API key, relay, Bedrock and Vertex accounts, and bodies with images, documents, thinking blocks, citations, MCP or unrecognized fields.

Profiles apply to both mimicked and native Claude Code preparation. They transfer because a body's tokens-per-byte ratio is a weighted average of its parts, and both preparations add only text and JSON whose ratios were measured. This is an argument from the measured content classes, not a native measurement.

Calibrated profiles exist only for `claude-sonnet-4-6` and `claude-opus-4-6` on Anthropic OAuth accounts without a custom base URL. The profiles were calibrated on 2026-11-05 with one OAuth account and a frozen synthetic corpus: 48 samples per model, 24 for fitting and 24 held out. The samples and a refit test are in `backend/internal/service/testdata/long_context_calibration/`. The bounds are empirical. No held-out sample broke them, but one highly compressible fixture counted below the lower bound, so such input can be rejected near the window even though it would fit.

Measured body-size bands for step 2. Bodies at or below the first number are admitted. Bodies at or above the second are rejected. Everything in between goes to step 3.

| max_tokens | claude-sonnet-4-6 | claude-opus-4-6 |
| --- | --- | --- |
| 1,024 | ≤ 127,614 / ≥ 2,108,952 bytes | ≤ 127,613 / ≥ 2,107,929 bytes |
| 8,192 | ≤ 122,604 / ≥ 2,032,979 | ≤ 122,604 / ≥ 2,031,993 |
| 32,000 | ≤ 105,965 / ≥ 1,780,638 | ≤ 105,964 / ≥ 1,779,775 |
| 64,000 | ≤ 83,600 / ≥ 1,441,471 | ≤ 83,599 / ≥ 1,440,772 |

The bytes are the final prepared body, including the gateway's own additions. Text with known fields is eligible for steps 2 and 3. `context_management`, which OAuth mimicry adds when thinking is on, is allowed because it can only remove input.

## Observe log

Every gated attempt writes one `long_context.observe` line (level info) when it finishes. It records:

- the attempt: entry point, model, route, whether it was mimicked, account ID and effective policy action;
- the decision: eligibility (`eligible`, `unprofiled` or `ineligible`) and the decision itself (`profile_admit`, `profile_reject`, `estimate_admit`, `estimate_reject`, `unprofiled_admit`, `ineligible_admit`, `output_reject`, `marker_reject`, `policy_pass` or `policy_unavailable`);
- the numbers behind it: body bytes, `max_tokens`, input budget, the profile's lower and upper bounds, and the estimate;
- what actually happened: the outcome and, when inference ran, the provider's actual `input_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, their total, and `actual_over_budget`.

Prompt content and credentials are never logged. Comparing the actual total with the estimate and bounds shows false rejections and slips on real traffic.

## Calibration

`backend/cmd/longctx-calibrate` (build tag `calibration`, never part of a normal build) runs the bounded count-only experiment on the gateway host. It reads the approved account through a read-only database session without running migrations, and it prepares each fixture with the gateway's own Messages preparation. Each request goes through the account's own proxy and TLS profile, redirects are refused, and every request is checked before sending to make sure it goes to `api.anthropic.com` with a bearer credential. Without `-live` it only prepares and checks the requests and prints the manifest hash. With `-live` it records every attempt in `$HOME/longctx-calibration-139/ledger.jsonl` before sending it. The ledger binds the manifest and the 98-attempt budget. A stop (401, 403, 429, a redirect, two transport failures in a row, or a failed readiness check) is permanent. Nothing is ever retried. A crash that leaves an attempt without an outcome blocks further runs. `report.json` holds the fitted coefficients, held-out violations, false rejections and byte bands, or a fit error if any sample is missing.

## Client errors

No retry, account switch, account state change or Retry-After header is involved. After a keepalive has been sent, the error is a single terminal SSE event in the client's protocol.

| Status | Code | Meaning |
| --- | --- | --- |
| 400 `invalid_request_error` | `long_context_disabled` | the 1M marker was sent, or the request exceeds the standard window |
| 503 `api_error` | `long_context_policy_unavailable` | the policy could not be read |

The public `/v1/messages/count_tokens` endpoint is unchanged. A count it returns does not grant inference.

Decisions are logged as `long_context.observe` (see above).
