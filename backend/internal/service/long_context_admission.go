package service

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Long context admission (#139). The existing beta-policy rule for the 1M marker is the
// only switch: block and filter also gate the actual input size against the model's own
// context window (max_input_tokens in the pricing data, 1M for 4.6 and later, 200K for 4.5
// and earlier), pass is the administrator's explicit opt-in to long context. The gate runs on the exact
// body about to be sent to inference, so it does not depend on the client's beta header.
const (
	longContextBetaToken = "context-1m-2025-08-07"

	// input + max_tokens + reserve must fit the model's window.
	longContextReserve = 8_192
	// Framing/structure allowance added around a profile's byte-based estimate.
	longContextFramingAllowance = 8_192
	// Bodies a profile cannot decide are estimated with the highest realistic ratio
	// measured in lc-corpus-v1 (0.367 tokens/byte) plus 10%: 0.4037 (#139, option C).
	longContextRealisticNum = 4037
	longContextRealisticDen = 10000

	GatewayFailureReasonLongContextDisabled          GatewayFailureReason = "long_context_disabled"
	GatewayFailureReasonLongContextCountUnavailable  GatewayFailureReason = "long_context_count_unavailable"
	GatewayFailureReasonLongContextPolicyUnavailable GatewayFailureReason = "long_context_policy_unavailable"

	longContextDisabledText        = "The request exceeds the model's context window (input plus max_tokens). Shorten or compact the conversation, or lower max_tokens."
	longContextMarkerDisabledText  = "Long context (1M) is disabled on this gateway. Remove the context-1m beta and keep the request within the model's context window."
	longContextPolicyTransientText = "The gateway could not read its long context policy, so the request was not sent. Try again shortly."
	longContextMaxTokensText       = "max_tokens must be a positive integer."
)

// IsLongContextStop reports a stop that the handler answers with the long context
// contract: no account switch, protocol-aware error, stable code.
func (e *UpstreamFailoverError) IsLongContextStop() bool {
	if e == nil || e.NextAccountAction != NextAccountStop {
		return false
	}
	switch e.Reason {
	case GatewayFailureReasonLongContextCredits,
		GatewayFailureReasonLongContextDisabled,
		GatewayFailureReasonLongContextCountUnavailable,
		GatewayFailureReasonLongContextPolicyUnavailable:
		return true
	}
	return false
}

// ClientErrorType is the client-facing error type of a long context stop.
func (e *UpstreamFailoverError) ClientErrorType() string {
	if e != nil && e.ClientStatusCode >= 500 {
		return "api_error"
	}
	return "invalid_request_error"
}

func newLongContextLocalStop(reason GatewayFailureReason, status int, message string) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            reason,
		NextAccountAction: NextAccountStop,
		ClientStatusCode:  status,
		ClientMessage:     message,
	}
}

// longContextActionRank orders actions from most to least restrictive.
func longContextActionRank(action string) int {
	switch action {
	case BetaPolicyActionBlock:
		return 3
	case BetaPolicyActionFilter:
		return 2
	case BetaPolicyActionPass:
		return 1
	}
	return 0
}

// resolveLongContextAction returns the effective action of the 1M rule for this account
// and final model. A missing rule means block (the default); a rule whose scope does not
// cover the account means pass, because a deliberately narrow scope keeps its meaning.
// Duplicate rules resolve to the most restrictive applicable action.
func resolveLongContextAction(settings *BetaPolicySettings, account *Account, model string) string {
	if settings == nil {
		return BetaPolicyActionBlock
	}
	found := false
	effective := ""
	for _, rule := range migrateLegacyLongContextRules(settings.Rules) {
		if strings.TrimSpace(rule.BetaToken) != longContextBetaToken {
			continue
		}
		found = true
		if !betaPolicyScopeMatches(rule.Scope, account.IsOAuth(), account.IsBedrock()) {
			continue
		}
		action, _ := resolveRuleAction(rule, model)
		if longContextActionRank(action) == 0 {
			action = BetaPolicyActionBlock // unknown action never opts in
		}
		if longContextActionRank(action) > longContextActionRank(effective) {
			effective = action
		}
	}
	if !found {
		return BetaPolicyActionBlock
	}
	if effective == "" {
		return BetaPolicyActionPass // out of the configured scope
	}
	return effective
}

// legacyLongContextDefaultRule is the 1M rule earlier versions generated as the default.
var legacyLongContextDefaultRule = BetaPolicyRule{
	BetaToken: longContextBetaToken,
	Action:    BetaPolicyActionPass,
	Scope:     BetaPolicyScopeAll,
	ModelWhitelist: []string{
		"claude-sonnet-5", "claude-sonnet-5-*", "claude-sonnet-5@*",
		"us.anthropic.claude-sonnet-5*", "eu.anthropic.claude-sonnet-5*", "apac.anthropic.claude-sonnet-5*",
		"jp.anthropic.claude-sonnet-5*", "au.anthropic.claude-sonnet-5*", "us-gov.anthropic.claude-sonnet-5*",
		"global.anthropic.claude-sonnet-5*", "anthropic.claude-sonnet-5*",
	},
	FallbackAction: BetaPolicyActionFilter,
}

func isLegacyLongContextDefaultRule(rule BetaPolicyRule) bool {
	l := legacyLongContextDefaultRule
	if strings.TrimSpace(rule.BetaToken) != l.BetaToken || rule.Action != l.Action || rule.Scope != l.Scope ||
		rule.FallbackAction != l.FallbackAction || rule.ErrorMessage != "" || rule.FallbackErrorMessage != "" ||
		len(rule.ModelWhitelist) != len(l.ModelWhitelist) {
		return false
	}
	want := make(map[string]struct{}, len(l.ModelWhitelist))
	for _, p := range l.ModelWhitelist {
		want[p] = struct{}{}
	}
	for _, p := range rule.ModelWhitelist {
		if _, ok := want[strings.TrimSpace(p)]; !ok {
			return false
		}
		delete(want, strings.TrimSpace(p))
	}
	return len(want) == 0
}

// migrateLegacyLongContextRules replaces only the exact old generated 1M default with the
// new default. It is applied when settings are read, so the admin page and the gateway see
// the same effective rule and nothing is written behind the administrator's back; the next
// save persists it. A customized rule is never touched.
func migrateLegacyLongContextRules(rules []BetaPolicyRule) []BetaPolicyRule {
	idx := -1
	for i, rule := range rules {
		if isLegacyLongContextDefaultRule(rule) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return rules
	}
	out := append([]BetaPolicyRule(nil), rules...)
	for i := range out {
		if isLegacyLongContextDefaultRule(out[i]) {
			out[i] = defaultLongContextRule()
		}
	}
	return out
}

func defaultLongContextRule() BetaPolicyRule {
	return BetaPolicyRule{BetaToken: longContextBetaToken, Action: BetaPolicyActionBlock, Scope: BetaPolicyScopeAll}
}

// longContextRoute names where the counter (and a calibrated profile) can come from.
type longContextRoute string

const (
	longContextRouteAnthropicOAuth longContextRoute = "anthropic_oauth" // official endpoint, OAuth/setup token
	longContextRouteOther          longContextRoute = "other"           // API key, relay, Bedrock, Vertex: no calibrated profile
)

// longContextProfile is an empirically calibrated byte->token interval for one route and an
// explicit list of effective model IDs. Coefficients are rationals: tokens ≈ bytes*Num/Den.
type longContextProfile struct {
	Name     string
	Route    longContextRoute
	Models   []string
	LowNum   int64
	LowDen   int64
	HighNum  int64
	HighDen  int64
	Evidence string
}

// longContextProfiles holds reviewed calibration results. Empty means every request that is
// not already over budget by output arithmetic goes to the counter.
//
// Calibrated 2026-11-05 on one Anthropic OAuth account with the frozen synthetic corpus
// lc-corpus-v1 (48 samples per model, 24 fitting / 24 held-out); reviewed. Evidence and a
// refit test: testdata/long_context_calibration/lc-corpus-v1.json. Empirical bounds, not a
// tokenizer: no held-out sample broke them, and one adversarial fixture (many_blocks-3,
// highly compressible) fell below the lower bound, so such input may be rejected as too
// long once it nears the window. Only these exact model IDs on this route use them.
var longContextProfiles = []longContextProfile{
	{
		Name: "anthropic-oauth-claude-sonnet-4-6-lc-corpus-v1", Route: longContextRouteAnthropicOAuth, Models: []string{"claude-sonnet-4-6"},
		LowNum: 389, LowDen: 4123, HighNum: 151447, HighDen: 105847,
		Evidence: "testdata/long_context_calibration/lc-corpus-v1.json",
	},
	{
		Name: "anthropic-oauth-claude-opus-4-6-lc-corpus-v1", Route: longContextRouteAnthropicOAuth, Models: []string{"claude-opus-4-6"},
		LowNum: 389, LowDen: 4121, HighNum: 302894, HighDen: 211693,
		Evidence: "testdata/long_context_calibration/lc-corpus-v1.json",
	},
}

func findLongContextProfile(route longContextRoute, model string) *longContextProfile {
	for i := range longContextProfiles {
		p := &longContextProfiles[i]
		if p.Route != route {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return p
			}
		}
	}
	return nil
}

type longContextLocalDecision int

const (
	longContextUncertain longContextLocalDecision = iota
	longContextAdmit
	longContextReject
)

// longContextInputBudget returns T = W - O - R; negative means over budget by output alone.
func longContextInputBudget(window, maxTokens int64) int64 {
	return window - maxTokens - int64(longContextReserve)
}

// longContextWindow returns the model's context window from the pricing data, or 0 with
// source "unknown" when the model is not identified there (no numeric gate then).
func (s *GatewayService) longContextWindow(model string) (int64, string) {
	if s.billingService != nil {
		if p := s.billingService.pricingService.GetIdentifiedModelPricing(model); p != nil && p.MaxInputTokens > 0 {
			return int64(p.MaxInputTokens), "pricing"
		}
	}
	return 0, "unknown"
}

// decideLongContextLocally applies L=max(0,floor(a_low*B-H)) and U=ceil(a_high*B+H): admit
// when U <= T, reject when L > T, otherwise the counter decides. All arithmetic is exact.
func decideLongContextLocally(bodyBytes int, budget int64, profile *longContextProfile) longContextLocalDecision {
	if budget < 0 {
		return longContextReject
	}
	if profile == nil || profile.LowDen <= 0 || profile.HighDen <= 0 || profile.LowNum < 0 || profile.HighNum <= 0 {
		return longContextUncertain
	}
	b := big.NewInt(int64(bodyBytes))
	h := big.NewInt(longContextFramingAllowance)
	t := big.NewInt(budget)

	// U = ceil(HighNum*B/HighDen) + H
	u := new(big.Int).Mul(b, big.NewInt(profile.HighNum))
	den := big.NewInt(profile.HighDen)
	u.Add(u, new(big.Int).Sub(den, big.NewInt(1))).Quo(u, den).Add(u, h)
	if u.Cmp(t) <= 0 {
		return longContextAdmit
	}
	// L = max(0, floor(LowNum*B/LowDen) - H)
	l := new(big.Int).Mul(b, big.NewInt(profile.LowNum))
	l.Quo(l, big.NewInt(profile.LowDen)).Sub(l, h)
	if l.Sign() > 0 && l.Cmp(t) > 0 {
		return longContextReject
	}
	return longContextUncertain
}

// isLongContextLocallyEligible accepts only an explicitly understood schema: plain text,
// local function tools and their text results, with known keys at every level. Anything
// else (images, documents, thinking, server tools, MCP, unknown fields) cannot be bounded
// from bytes and must be counted. Nothing is stripped to make a body eligible.
func isLongContextLocallyEligible(body []byte) bool {
	root := gjson.ParseBytes(body)
	if !root.IsObject() || !longContextKeysAllowed(root, longContextRootKeys) {
		return false
	}
	if sys := root.Get("system"); sys.Exists() && !isLongContextTextContent(sys) {
		return false
	}
	if tools := root.Get("tools"); tools.Exists() && !longContextEach(tools, isLongContextLocalTool) {
		return false
	}
	messages := root.Get("messages")
	return messages.IsArray() && longContextEach(messages, func(msg gjson.Result) bool {
		return msg.IsObject() && longContextKeysAllowed(msg, longContextMessageKeys) && isLongContextMessageContent(msg.Get("content"))
	})
}

func longContextSet(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

var (
	// Generation/config fields do not add input. context_management (which OAuth mimicry
	// adds whenever thinking is on) can only remove input server-side, so a bound on the
	// sent bytes stays an upper bound. Anything not listed has no local bound.
	longContextRootKeys = longContextSet("model", "max_tokens", "messages", "system", "tools", "tool_choice",
		"metadata", "stream", "temperature", "top_p", "top_k", "stop_sequences", "thinking", "output_config",
		"context_management")
	longContextMessageKeys    = longContextSet("role", "content")
	longContextTextBlockKeys  = longContextSet("type", "text", "cache_control")
	longContextToolUseKeys    = longContextSet("type", "id", "name", "input", "cache_control")
	longContextToolResultKeys = longContextSet("type", "tool_use_id", "content", "is_error", "cache_control")
	longContextToolKeys       = longContextSet("type", "name", "description", "input_schema", "cache_control")
)

func longContextKeysAllowed(obj gjson.Result, allowed map[string]struct{}) bool {
	ok := true
	obj.ForEach(func(k, _ gjson.Result) bool {
		_, ok = allowed[k.String()]
		return ok
	})
	return ok
}

func longContextEach(arr gjson.Result, fn func(gjson.Result) bool) bool {
	if !arr.IsArray() {
		return false
	}
	ok := true
	arr.ForEach(func(_, v gjson.Result) bool {
		ok = fn(v)
		return ok
	})
	return ok
}

func isLongContextLocalTool(tool gjson.Result) bool {
	if !tool.IsObject() || !longContextKeysAllowed(tool, longContextToolKeys) {
		return false
	}
	t := tool.Get("type")
	return (!t.Exists() || t.String() == "custom") && tool.Get("name").Type == gjson.String
}

func isLongContextTextBlock(block gjson.Result) bool {
	return block.IsObject() && block.Get("type").String() == "text" && block.Get("text").Type == gjson.String &&
		longContextKeysAllowed(block, longContextTextBlockKeys)
}

func isLongContextTextContent(v gjson.Result) bool {
	if v.Type == gjson.String {
		return true
	}
	return longContextEach(v, isLongContextTextBlock)
}

func isLongContextMessageContent(v gjson.Result) bool {
	if v.Type == gjson.String {
		return true
	}
	return longContextEach(v, func(block gjson.Result) bool {
		switch block.Get("type").String() {
		case "text":
			return isLongContextTextBlock(block)
		case "tool_use":
			return block.IsObject() && longContextKeysAllowed(block, longContextToolUseKeys)
		case "tool_result":
			if !block.IsObject() || !longContextKeysAllowed(block, longContextToolResultKeys) {
				return false
			}
			c := block.Get("content")
			return !c.Exists() || isLongContextTextContent(c)
		}
		return false
	})
}

// longContextAttempt is one prepared inference attempt.
type longContextAttempt struct {
	Entry   string // messages, passthrough, chat_completions, responses, bedrock
	Account *Account
	Model   string // final effective upstream model
	Body    []byte // exact inference body
	Header  http.Header
	Route   longContextRoute
	Mimic   bool // prepared with Claude Code mimicry (non Claude Code client on OAuth)
}

// longContextObservation is what one admission saw and decided. It is logged once with the
// attempt's outcome, including the provider's actual input usage, so estimates can be
// checked against real traffic. Sizes and counts only; never content or credentials.
type longContextObservation struct {
	Entry        string
	Model        string
	Route        string
	Mimic        bool
	AccountID    int64
	Action       string // effective policy action
	Eligibility  string // eligible | unprofiled | ineligible
	Decision     string
	BodyBytes    int
	MaxTokens    int64
	Window       int64 // model context window, 0 if unknown
	WindowSource string
	Budget       int64 // T = window - max_tokens - reserve
	Lower        int64 // profile L, -1 if not computed
	Upper        int64 // profile U, -1 if not computed
	Estimate     int64 // realistic estimate, -1 if not computed
	// Upstream error response, when inference was rejected (0 = none seen).
	UpstreamStatus    int
	UpstreamErrorType string
	PromptTooLong     bool
	// Attempt numbers the admissions of one client request (internal retries and account
	// switches), from 1.
	Attempt int
}

const (
	longContextObservationKey = "longContextObservation"
	longContextAttemptKey     = "longContextAttempt"
)

// longContextPreparationOnlyKey marks a context in which Forward only prepares a body and
// sends it to a local capture, never upstream. Only the calibration harness (test and
// calibration builds) sets it; nothing in a production build does.
type longContextPreparationOnlyKey struct{}

func longContextRouteFor(account *Account) longContextRoute {
	if account == nil || account.Platform != PlatformAnthropic || account.IsBedrock() || account.Type == AccountTypeServiceAccount {
		return longContextRouteOther
	}
	if account.IsOAuth() && !account.IsCustomBaseURLEnabled() {
		return longContextRouteAnthropicOAuth
	}
	return longContextRouteOther // API key, relays and custom base URLs: no profile
}

func hasLongContextMarker(header http.Header, body []byte) bool {
	if header != nil {
		for _, v := range header.Values("anthropic-beta") {
			if containsBetaToken(v, longContextBetaToken) {
				return true
			}
		}
		if v := getHeaderRaw(header, "anthropic-beta"); containsBetaToken(v, longContextBetaToken) {
			return true
		}
	}
	found := false
	gjson.GetBytes(body, "anthropic_beta").ForEach(func(_, v gjson.Result) bool {
		found = strings.TrimSpace(v.String()) == longContextBetaToken
		return !found
	})
	return found
}

// admitLongContext decides locally, with no upstream call, whether a prepared attempt may
// reach inference. A nil result admits it; otherwise the result is a request stop that the
// handler answers directly.
func (s *GatewayService) admitLongContext(ctx context.Context, c *gin.Context, a longContextAttempt) error {
	if a.Account == nil || a.Account.Platform != PlatformAnthropic || s.settingService == nil || ctx.Value(longContextPreparationOnlyKey{}) != nil {
		return nil
	}
	obs := &longContextObservation{Entry: a.Entry, Model: a.Model, Route: string(a.Route), Mimic: a.Mimic, AccountID: a.Account.ID,
		BodyBytes: len(a.Body), MaxTokens: -1, Budget: -1, Lower: -1, Upper: -1, Estimate: -1}
	if c != nil {
		// An internal retry re-admits within one Forward: write out the attempt it replaces
		// first, so a failed attempt followed by a successful retry keeps both outcomes.
		if prev := takeLongContextObservation(c); prev != nil {
			logLongContextObservation(c, prev, nil, nil, "superseded_by_retry")
		}
		obs.Attempt = c.GetInt(longContextAttemptKey) + 1
		c.Set(longContextAttemptKey, obs.Attempt)
		c.Set(longContextObservationKey, obs)
	}
	settings, err := s.settingService.GetBetaPolicySettings(ctx)
	if err != nil {
		obs.Decision = "policy_unavailable"
		return newLongContextLocalStop(GatewayFailureReasonLongContextPolicyUnavailable, http.StatusServiceUnavailable, longContextPolicyTransientText)
	}
	obs.Action = resolveLongContextAction(settings, a.Account, a.Model)
	if obs.Action == BetaPolicyActionPass && isNoCreditOAuthAccount(a.Account) {
		// Subscription OAuth has no credits (#147): a pass rule, a whitelist or a narrow scope
		// does not open 1M here; the standard-window admission still runs.
		obs.Action = BetaPolicyActionFilter
	}
	if obs.Action == BetaPolicyActionPass {
		obs.Decision = "policy_pass"
		return nil
	}
	// The client's own marker counts even when the outgoing header drops it.
	clientAsked := c != nil && c.Request != nil && hasLongContextMarker(c.Request.Header, nil)
	if obs.Action == BetaPolicyActionBlock && (clientAsked || hasLongContextMarker(a.Header, a.Body)) {
		obs.Decision = "marker_reject"
		return s.longContextReject(c, longContextMarkerDisabledText)
	}
	return s.decideLongContext(c, a, obs)
}

func (s *GatewayService) decideLongContext(c *gin.Context, a longContextAttempt, obs *longContextObservation) error {
	maxTokens := gjson.GetBytes(a.Body, "max_tokens")
	if maxTokens.Type != gjson.Number || maxTokens.Int() <= 0 || float64(maxTokens.Int()) != maxTokens.Float() {
		obs.Decision = "invalid_max_tokens"
		return s.longContextReject(c, longContextMaxTokensText)
	}
	obs.MaxTokens = maxTokens.Int()
	obs.Window, obs.WindowSource = s.longContextWindow(a.Model)
	if obs.Window == 0 {
		obs.Decision = "unknown_window_admit"
		return nil
	}
	obs.Budget = longContextInputBudget(obs.Window, obs.MaxTokens)
	if obs.Budget < 0 {
		obs.Decision = "output_reject"
		return s.longContextReject(c, longContextDisabledText)
	}
	b := int64(len(a.Body))

	var profile *longContextProfile
	switch {
	case !isLongContextLocallyEligible(a.Body):
		obs.Eligibility = "ineligible"
	default:
		if profile = findLongContextProfile(a.Route, a.Model); profile == nil {
			obs.Eligibility = "unprofiled"
		} else {
			obs.Eligibility = "eligible"
		}
	}
	if profile == nil {
		// shortcut: content or model without a calibrated bound is admitted (only output
		// arithmetic and the marker apply); the observe log shows how often and how large.
		if obs.Eligibility == "unprofiled" {
			obs.Estimate = longContextRealisticEstimate(b) // recorded only, never decides
		}
		obs.Decision = obs.Eligibility + "_admit"
		return nil
	}
	obs.Lower, obs.Upper = longContextInterval(profile, b)
	switch decideLongContextLocally(int(b), obs.Budget, profile) {
	case longContextAdmit:
		obs.Decision = "profile_admit"
		return nil
	case longContextReject:
		obs.Decision = "profile_reject"
		return s.longContextReject(c, longContextDisabledText)
	}
	obs.Estimate = longContextRealisticEstimate(b)
	if obs.Estimate > obs.Budget {
		obs.Decision = "estimate_reject"
		return s.longContextReject(c, longContextDisabledText)
	}
	obs.Decision = "estimate_admit"
	return nil
}

// longContextInterval returns the profile's L and U for a body of b bytes, exactly as
// decideLongContextLocally compares them.
func longContextInterval(p *longContextProfile, b int64) (lo, up int64) {
	u := new(big.Int).Mul(big.NewInt(b), big.NewInt(p.HighNum))
	den := big.NewInt(p.HighDen)
	u.Add(u, new(big.Int).Sub(den, big.NewInt(1))).Quo(u, den).Add(u, big.NewInt(longContextFramingAllowance))
	l := new(big.Int).Mul(big.NewInt(b), big.NewInt(p.LowNum))
	l.Quo(l, big.NewInt(p.LowDen)).Sub(l, big.NewInt(longContextFramingAllowance))
	if l.Sign() < 0 {
		l.SetInt64(0)
	}
	return l.Int64(), u.Int64()
}

// longContextRealisticEstimate is ceil(b * 0.4037) + H.
func longContextRealisticEstimate(b int64) int64 {
	e := new(big.Int).Mul(big.NewInt(b), big.NewInt(longContextRealisticNum))
	den := big.NewInt(longContextRealisticDen)
	e.Add(e, new(big.Int).Sub(den, big.NewInt(1))).Quo(e, den).Add(e, big.NewInt(longContextFramingAllowance))
	return e.Int64()
}

func (s *GatewayService) longContextReject(c *gin.Context, message string) error {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	return newLongContextLocalStop(GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, message)
}

// noteLongContextUpstreamError records an upstream error response on the attempt's
// observation, so the observe line shows when the provider itself rejected the size.
func noteLongContextUpstreamError(c *gin.Context, status int, body []byte) {
	if c == nil {
		return
	}
	v, ok := c.Get(longContextObservationKey)
	if !ok {
		return
	}
	if obs, _ := v.(*longContextObservation); obs != nil {
		obs.UpstreamStatus = status
		obs.UpstreamErrorType = gjson.GetBytes(body, "error.type").String()
		obs.PromptTooLong = strings.Contains(strings.ToLower(gjson.GetBytes(body, "error.message").String()), "prompt is too long")
	}
}

// LogLongContextOutcome writes one long_context.observe line for the attempt that just
// finished: what admission decided and, when inference ran, the provider's actual input
// usage. It is the data for checking the estimates against real traffic.
func LogLongContextOutcome(c *gin.Context, result *ForwardResult, err error) {
	if c == nil {
		return
	}
	if obs := takeLongContextObservation(c); obs != nil {
		logLongContextObservation(c, obs, result, err, "")
	}
}

// endLongContextAttemptWithoutResponse writes the current attempt as a retry whose transport
// failed, before an earlier attempt's response is restored for the client: that response
// was already recorded on its own (superseded) line and must not be attributed to this one.
func endLongContextAttemptWithoutResponse(c *gin.Context) {
	if c == nil {
		return
	}
	if obs := takeLongContextObservation(c); obs != nil {
		logLongContextObservation(c, obs, nil, nil, "retry_transport_error")
	}
}

// takeLongContextObservation removes and returns the attempt's pending observation.
func takeLongContextObservation(c *gin.Context) *longContextObservation {
	v, ok := c.Get(longContextObservationKey)
	if !ok {
		return nil
	}
	c.Set(longContextObservationKey, nil)
	obs, _ := v.(*longContextObservation)
	return obs
}

// logLongContextObservation writes one observe line. outcome, when set, is a fixed outcome
// for an attempt that ended without a final result (no usage is reported for it).
func logLongContextObservation(c *gin.Context, obs *longContextObservation, result *ForwardResult, err error, outcome string) {
	fields := []zap.Field{zap.Int("attempt", obs.Attempt),
		zap.String("entry", obs.Entry), zap.String("model", obs.Model), zap.String("route", obs.Route),
		zap.Bool("mimic", obs.Mimic), zap.Int64("account_id", obs.AccountID), zap.String("action", obs.Action),
		zap.String("eligibility", obs.Eligibility), zap.String("decision", obs.Decision),
		zap.Int("body_bytes", obs.BodyBytes), zap.Int64("max_tokens", obs.MaxTokens),
		zap.Int64("window", obs.Window), zap.String("window_source", obs.WindowSource), zap.Int64("input_budget", obs.Budget),
		zap.Int64("profile_lower", obs.Lower), zap.Int64("profile_upper", obs.Upper), zap.Int64("estimate", obs.Estimate),
	}
	if result != nil && outcome == "" {
		u := result.Usage
		total := int64(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens)
		fields = append(fields, zap.String("outcome", "inference"),
			zap.Int("input_tokens", u.InputTokens), zap.Int("cache_creation_input_tokens", u.CacheCreationInputTokens),
			zap.Int("cache_read_input_tokens", u.CacheReadInputTokens), zap.Int64("total_input_tokens", total))
		if obs.Budget >= 0 {
			fields = append(fields, zap.Bool("actual_over_budget", total > obs.Budget))
		}
	} else if err != nil || outcome != "" {
		if obs.UpstreamStatus != 0 {
			fields = append(fields, zap.Int("upstream_status", obs.UpstreamStatus),
				zap.String("upstream_error_type", obs.UpstreamErrorType), zap.Bool("prompt_too_long", obs.PromptTooLong))
		}
		var stop *UpstreamFailoverError
		if outcome == "" {
			outcome = "error"
			if errors.As(err, &stop) && stop.Reason != "" {
				outcome = string(stop.Reason)
			}
		}
		fields = append(fields, zap.String("outcome", outcome))
	}
	log := logger.L()
	if c.Request != nil {
		log = logger.FromContext(c.Request.Context()) // carries client_request_id
	}
	log.Info("long_context.observe", fields...)
}

// anthropicLongContextAttempt describes a Messages-shaped attempt built by buildUpstreamRequest.
func anthropicLongContextAttempt(entry string, account *Account, model string, body []byte, header http.Header, mimic bool) longContextAttempt {
	return longContextAttempt{Entry: entry, Account: account, Model: model, Body: body, Header: header, Route: longContextRouteFor(account), Mimic: mimic}
}
