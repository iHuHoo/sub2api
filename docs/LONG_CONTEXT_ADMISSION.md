# Long context admission

The beta policy rule for `context-1m-2025-08-07` (Admin → Settings → Beta policy) is the gateway's long context switch. There is no separate setting.

| Action | Request with the 1M marker | Request without the marker |
| --- | --- | --- |
| block (default) | rejected before any upstream call | checked against the standard window |
| filter | marker removed | checked against the standard window |
| pass | forwarded as before | long context allowed (explicit opt-in) |

The rule applies only to the accounts in its scope and the models in its whitelist; anything outside is not protected. A missing rule, missing or unreadable-as-JSON settings, and the old generated default (pass for Sonnet 5, filter otherwise) all mean block for every scope. The old default is replaced when settings are read, and saving the page persists it. A customized rule is kept as it is.

## The standard window check

It runs on the exact body about to be sent to inference: after protocol conversion (Chat Completions, Responses), model mapping, OAuth mimicry and every rewritten retry. The request is admitted when input tokens + `max_tokens` + 8,192 ≤ 200,000. These numbers are a local policy, not a provider limit.

1. If `max_tokens` alone leaves no room, it is rejected locally.
2. For plain text and local function tools, a reviewed calibration profile for the exact route and model can admit a clearly small request or reject a clearly huge one without a counter call. The profile is empirical, not exact tokenization. Highly repetitive or padded input may be rejected even though it would fit.
3. Everything else is counted once with the same account's `count_tokens` endpoint, inside a 5-second budget per client request. This adds latency and counter traffic to the account.
4. Routes without a counter (Bedrock, Vertex) reject requests that step 2 cannot decide.

Until a profile has been calibrated and reviewed, step 2 never applies and every request that passes step 1 is counted.

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
