package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Long context admission (#139). The existing beta-policy rule for the 1M marker is the
// only switch: block and filter also gate the actual input size against a standard window,
// pass is the administrator's explicit opt-in to long context. The gate runs on the exact
// body about to be sent to inference, so it does not depend on the client's beta header.
const (
	longContextBetaToken = "context-1m-2025-08-07"

	// Local policy, not a provider limit: input + max_tokens + reserve must fit this window.
	// Every Anthropic model this gateway routes (Claude 3 and later) has a standard context
	// window of at least 200k, so no model needs a smaller one; a model with a smaller window
	// would need its own entry here before it is supported.
	longContextStandardWindow = 200_000
	longContextReserve        = 8_192
	// Framing/structure allowance added around a profile's byte-based estimate.
	longContextFramingAllowance = 8_192
	// Cumulative time a single client request may spend in counter calls.
	longContextCountBudget = 5 * time.Second
	// Counter responses are tiny; never buffer an unbounded body.
	longContextCountMaxResponseBytes = 64 << 10

	GatewayFailureReasonLongContextDisabled          GatewayFailureReason = "long_context_disabled"
	GatewayFailureReasonLongContextCountUnavailable  GatewayFailureReason = "long_context_count_unavailable"
	GatewayFailureReasonLongContextPolicyUnavailable GatewayFailureReason = "long_context_policy_unavailable"

	longContextDisabledText        = "Long context is disabled on this gateway: the request exceeds the standard context window (input plus max_tokens). Shorten or compact the conversation, or lower max_tokens."
	longContextMarkerDisabledText  = "Long context (1M) is disabled on this gateway. Remove the context-1m beta and keep the request within the standard context window."
	longContextCountPermanentText  = "The gateway could not verify this request's context length, so it was not sent: this route or content type cannot be counted. Shorten the request or remove unsupported content."
	longContextCountTransientText  = "The gateway could not verify this request's context length right now, so it was not sent. Try again shortly."
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
	longContextRouteAnthropicOAuth  longContextRoute = "anthropic_oauth"   // official endpoint, OAuth/setup token
	longContextRouteAnthropicAPIKey longContextRoute = "anthropic_api_key" // official or relay base URL
	longContextRouteNoCounter       longContextRoute = "no_counter"        // Bedrock, Vertex, anything else
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
var longContextProfiles []longContextProfile

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
func longContextInputBudget(maxTokens int64) int64 {
	return int64(longContextStandardWindow) - maxTokens - int64(longContextReserve)
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
	// Generation/config fields do not add input; anything not listed takes the counter path.
	longContextRootKeys = longContextSet("model", "max_tokens", "messages", "system", "tools", "tool_choice",
		"metadata", "stream", "temperature", "top_p", "top_k", "stop_sequences", "thinking", "output_config")
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
	Entry   string // messages, passthrough, chat_completions, responses, bedrock, vertex
	Account *Account
	Model   string // final effective upstream model
	Body    []byte // exact inference body
	Header  http.Header
	Route   longContextRoute
	// BuildCountRequest builds the counter request for Body; nil means no counter on this route.
	BuildCountRequest func(ctx context.Context) (*http.Request, error)
	ProxyURL          string
}

// longContextRequestState is request-local: the cumulative counter budget and decisions
// already made for identical attempts (same account, model, body).
type longContextRequestState struct {
	spent     time.Duration
	decisions map[[32]byte]error
}

const longContextRequestStateKey = "longContextAdmissionState"

func longContextStateFor(c *gin.Context) *longContextRequestState {
	if c != nil {
		if v, ok := c.Get(longContextRequestStateKey); ok {
			if st, ok := v.(*longContextRequestState); ok {
				return st
			}
		}
	}
	st := &longContextRequestState{decisions: map[[32]byte]error{}}
	if c != nil {
		c.Set(longContextRequestStateKey, st)
	}
	return st
}

func longContextAttemptKey(a longContextAttempt, action string) [32]byte {
	h := sha256.New()
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(a.Account.ID))
	_, _ = h.Write(id[:])
	_, _ = h.Write([]byte(a.Model + "\x00" + action + "\x00" + string(a.Route) + "\x00"))
	_, _ = h.Write(a.Body)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func longContextRouteFor(account *Account) longContextRoute {
	if account == nil || account.Platform != PlatformAnthropic || account.IsBedrock() || account.Type == AccountTypeServiceAccount {
		return longContextRouteNoCounter
	}
	if account.IsOAuth() {
		if account.IsCustomBaseURLEnabled() {
			return longContextRouteAnthropicAPIKey // relay: counter exists, official profile does not apply
		}
		return longContextRouteAnthropicOAuth
	}
	if account.Type == AccountTypeAPIKey {
		return longContextRouteAnthropicAPIKey
	}
	return longContextRouteNoCounter
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

// admitLongContext decides whether a prepared attempt may reach inference. A nil result
// admits it; otherwise the result is a request stop that the handler answers directly.
func (s *GatewayService) admitLongContext(ctx context.Context, c *gin.Context, a longContextAttempt) error {
	if a.Account == nil || a.Account.Platform != PlatformAnthropic || s.settingService == nil {
		return nil
	}
	settings, err := s.settingService.GetBetaPolicySettings(ctx)
	if err != nil {
		s.logLongContextDecision(c, a, "policy_unavailable", 0, 0, -1, 0)
		return newLongContextLocalStop(GatewayFailureReasonLongContextPolicyUnavailable, http.StatusServiceUnavailable, longContextPolicyTransientText)
	}
	action := resolveLongContextAction(settings, a.Account, a.Model)
	if action == BetaPolicyActionPass {
		return nil
	}
	// The client's own marker counts even when the outgoing header drops it.
	clientAsked := c != nil && c.Request != nil && hasLongContextMarker(c.Request.Header, nil)
	if action == BetaPolicyActionBlock && (clientAsked || hasLongContextMarker(a.Header, a.Body)) {
		s.logLongContextDecision(c, a, "marker_blocked", 0, 0, -1, 0)
		return s.longContextReject(c, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, longContextMarkerDisabledText)
	}

	state := longContextStateFor(c)
	key := longContextAttemptKey(a, action)
	if decided, ok := state.decisions[key]; ok {
		return decided
	}
	decided := s.decideLongContext(ctx, c, a, state)
	state.decisions[key] = decided
	return decided
}

func (s *GatewayService) decideLongContext(ctx context.Context, c *gin.Context, a longContextAttempt, state *longContextRequestState) error {
	maxTokens := gjson.GetBytes(a.Body, "max_tokens")
	if maxTokens.Type != gjson.Number || maxTokens.Int() <= 0 || float64(maxTokens.Int()) != maxTokens.Float() {
		return s.longContextReject(c, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, longContextMaxTokensText)
	}
	o := maxTokens.Int()
	budget := longContextInputBudget(o)
	b := len(a.Body)

	var profile *longContextProfile
	if isLongContextLocallyEligible(a.Body) {
		profile = findLongContextProfile(a.Route, a.Model)
	}
	switch decideLongContextLocally(b, budget, profile) {
	case longContextAdmit:
		s.logLongContextDecision(c, a, "local_admit", b, o, -1, budget)
		return nil
	case longContextReject:
		s.logLongContextDecision(c, a, "local_reject", b, o, -1, budget)
		return s.longContextReject(c, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, longContextDisabledText)
	}

	if a.BuildCountRequest == nil {
		s.logLongContextDecision(c, a, "no_counter", b, o, -1, budget)
		return s.longContextReject(c, GatewayFailureReasonLongContextCountUnavailable, http.StatusBadRequest, longContextCountPermanentText)
	}
	tokens, status, transient, err := s.countLongContextInput(ctx, a, state)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // client went away: no inference, nothing to answer
		}
		logger.L().Info("long_context.count_failed", zap.String("entry", a.Entry), zap.String("model", a.Model),
			zap.Int64("account_id", a.Account.ID), zap.Int("count_status", status), zap.Bool("transient", transient))
		if transient {
			return s.longContextReject(c, GatewayFailureReasonLongContextCountUnavailable, http.StatusServiceUnavailable, longContextCountTransientText)
		}
		return s.longContextReject(c, GatewayFailureReasonLongContextCountUnavailable, http.StatusBadRequest, longContextCountPermanentText)
	}
	if profile != nil {
		s.checkLongContextProfile(a, profile, b, tokens)
	}
	if tokens > budget {
		s.logLongContextDecision(c, a, "count_reject", b, o, tokens, budget)
		return s.longContextReject(c, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, longContextDisabledText)
	}
	s.logLongContextDecision(c, a, "count_admit", b, o, tokens, budget)
	return nil
}

func (s *GatewayService) longContextReject(c *gin.Context, reason GatewayFailureReason, status int, message string) error {
	if status < 500 {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	}
	return newLongContextLocalStop(reason, status, message)
}

// countLongContextInput makes at most one counter call for this attempt, inside the
// request's cumulative budget. transient tells 503 (try later) from 400 (cannot count).
func (s *GatewayService) countLongContextInput(ctx context.Context, a longContextAttempt, state *longContextRequestState) (tokens int64, status int, transient bool, err error) {
	remaining := longContextCountBudget - state.spent
	if remaining <= 0 {
		return 0, 0, true, errors.New("long context count budget exhausted")
	}
	// One attempt is one request: a redirect is never followed to another host.
	countCtx, cancel := context.WithTimeout(WithHTTPUpstreamRedirectsDisabled(ctx), remaining)
	defer cancel()
	started := time.Now()
	defer func() { state.spent += time.Since(started) }()

	req, err := a.BuildCountRequest(countCtx)
	if err != nil {
		return 0, 0, false, fmt.Errorf("build count request: %w", err)
	}
	resp, err := s.httpUpstream.DoWithTLS(req, a.ProxyURL, a.Account.ID, a.Account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(a.Account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return 0, 0, true, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := readLongContextCountBody(resp.Body)
	if readErr != nil {
		return 0, resp.StatusCode, true, readErr
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		n, err := parseLongContextCount(body)
		if err != nil {
			return 0, resp.StatusCode, true, err
		}
		return n, resp.StatusCode, false, nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden, resp.StatusCode >= 500:
		return 0, resp.StatusCode, true, fmt.Errorf("count status %d", resp.StatusCode)
	default:
		return 0, resp.StatusCode, false, fmt.Errorf("count status %d", resp.StatusCode)
	}
}

// readLongContextCountBody reads the whole counter response; an oversized one is an error,
// never a silently truncated document.
func readLongContextCountBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, longContextCountMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > longContextCountMaxResponseBytes {
		return nil, errors.New("count response too large")
	}
	return body, nil
}

// parseLongContextCount requires one complete JSON object whose input_tokens is a
// non-negative integer. Anything else fails closed.
func parseLongContextCount(body []byte) (int64, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var doc struct {
		InputTokens json.RawMessage `json:"input_tokens"`
	}
	if err := dec.Decode(&doc); err != nil {
		return 0, fmt.Errorf("invalid count response: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return 0, errors.New("invalid count response: trailing data")
	}
	// Only a bare JSON integer: not a string, null, fraction or exponent.
	n, err := strconv.ParseInt(string(doc.InputTokens), 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid count response: input_tokens is not a non-negative integer")
	}
	return n, nil
}

// checkLongContextProfile records, without content, a counted input outside the profile's
// interval. The counted result still governs the request; profiles change only by review.
func (s *GatewayService) checkLongContextProfile(a longContextAttempt, p *longContextProfile, bodyBytes int, tokens int64) {
	b := big.NewInt(int64(bodyBytes))
	c := big.NewInt(tokens)
	u := new(big.Int).Mul(b, big.NewInt(p.HighNum))
	u.Quo(u, big.NewInt(p.HighDen)).Add(u, big.NewInt(longContextFramingAllowance+1))
	l := new(big.Int).Mul(b, big.NewInt(p.LowNum))
	l.Quo(l, big.NewInt(p.LowDen)).Sub(l, big.NewInt(longContextFramingAllowance))
	if c.Cmp(u) > 0 || c.Cmp(l) < 0 {
		logger.L().Warn("long_context.profile_violation",
			zap.String("profile", p.Name), zap.String("model", a.Model), zap.String("entry", a.Entry),
			zap.Int("body_bytes", bodyBytes), zap.Int64("counted_tokens", tokens))
	}
}

// logLongContextDecision is the audit record: reason, entry, model, sizes and limit only,
// never prompt content or credentials. counted is the counted input, or -1 when not counted.
func (s *GatewayService) logLongContextDecision(c *gin.Context, a longContextAttempt, decision string, bodyBytes int, maxTokens, counted, budget int64) {
	fields := []zap.Field{
		zap.String("decision", decision), zap.String("entry", a.Entry), zap.String("model", a.Model),
		zap.String("route", string(a.Route)), zap.Int64("account_id", a.Account.ID),
		zap.Int("body_bytes", bodyBytes), zap.Int64("max_tokens", maxTokens),
		zap.Int64("counted", counted), zap.Int64("input_budget", budget),
	}
	if strings.HasSuffix(decision, "admit") {
		logger.L().Debug("long_context.admission", fields...)
		return
	}
	logger.L().Info("long_context.admission", fields...)
}

// anthropicLongContextAttempt describes a Messages-shaped attempt built by buildUpstreamRequest.
// The counter is the same account's count_tokens endpoint on the same final body.
func (s *GatewayService) anthropicLongContextAttempt(c *gin.Context, entry string, account *Account, model string, body []byte, header http.Header, token, tokenType string, mimic bool, proxyURL string) longContextAttempt {
	a := longContextAttempt{Entry: entry, Account: account, Model: model, Body: body, Header: header, Route: longContextRouteFor(account), ProxyURL: proxyURL}
	if a.Route != longContextRouteNoCounter {
		a.BuildCountRequest = func(ctx context.Context) (*http.Request, error) {
			req, _, err := s.buildCountTokensRequest(ctx, c, account, append([]byte(nil), body...), token, tokenType, model, mimic)
			return req, err
		}
	}
	return a
}

func (s *GatewayService) passthroughLongContextAttempt(c *gin.Context, account *Account, model string, body []byte, header http.Header, token, proxyURL string) longContextAttempt {
	return longContextAttempt{
		Entry: "passthrough", Account: account, Model: model, Body: body, Header: header,
		Route: longContextRouteAnthropicAPIKey, ProxyURL: proxyURL,
		BuildCountRequest: func(ctx context.Context) (*http.Request, error) {
			return s.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, append([]byte(nil), body...), token)
		},
	}
}
