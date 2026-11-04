//go:build unit

package service

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type calibrationEvidence struct {
	CorpusVersion string `json:"corpus_version"`
	Route         string `json:"route"`
	Models        map[string]struct {
		Samples []struct {
			Fixture string `json:"fixture"`
			Set     string `json:"set"`
			Kind    string `json:"kind"`
			B       int64  `json:"body_bytes"`
			C       int64  `json:"input_tokens"`
		} `json:"samples"`
		Profile struct {
			Low  [2]int64 `json:"low"`
			High [2]int64 `json:"high"`
		} `json:"profile"`
		Violations      []string            `json:"violations"`
		FalseRejections []string            `json:"false_rejections"`
		Bands           map[string][2]int64 `json:"bands_fastpass_max_reject_min"`
	} `json:"models"`
}

func loadCalibrationEvidence(t *testing.T) calibrationEvidence {
	t.Helper()
	data, err := os.ReadFile("testdata/long_context_calibration/lc-corpus-v1.json")
	require.NoError(t, err)
	var ev calibrationEvidence
	require.NoError(t, json.Unmarshal(data, &ev))
	return ev
}

// The shipped coefficients are exactly what the checked-in samples produce, with the
// recorded held-out results and byte bands.
func TestLongContextProfilesMatchCalibrationEvidence(t *testing.T) {
	ev := loadCalibrationEvidence(t)
	require.Equal(t, calibrationCorpusVersion, ev.CorpusVersion)
	require.Equal(t, string(longContextRouteAnthropicOAuth), ev.Route)
	require.Len(t, longContextProfiles, 2)
	for _, model := range calibrationModels {
		m, ok := ev.Models[model]
		require.True(t, ok, model)
		var samples []calibrationSample
		for _, s := range m.Samples {
			samples = append(samples, calibrationSample{Fixture: s.Fixture, Set: s.Set, Kind: s.Kind, B: s.B, C: s.C})
		}
		res, err := fitCalibrationProfile("refit", longContextRouteAnthropicOAuth, model, samples, calibrationExpectedCounts)
		require.NoError(t, err, model)
		p := findLongContextProfile(longContextRouteAnthropicOAuth, model)
		require.NotNil(t, p, model)
		require.Equal(t, [4]int64{res.Profile.LowNum, res.Profile.LowDen, res.Profile.HighNum, res.Profile.HighDen},
			[4]int64{p.LowNum, p.LowDen, p.HighNum, p.HighDen}, model)
		require.Equal(t, [4]int64{m.Profile.Low[0], m.Profile.Low[1], m.Profile.High[0], m.Profile.High[1]},
			[4]int64{p.LowNum, p.LowDen, p.HighNum, p.HighDen}, model)
		require.Empty(t, res.Violations, model)
		require.Empty(t, m.Violations, model)
		require.Len(t, res.FalseRejections, 1, model)
		require.True(t, strings.HasPrefix(res.FalseRejections[0], "many_blocks-3:"), res.FalseRejections[0])
		require.Equal(t, m.FalseRejections, res.FalseRejections)
		for o, band := range m.Bands {
			maxTokens, err := strconv.ParseInt(o, 10, 64)
			require.NoError(t, err)
			fast, rej := calibrationBands(p, maxTokens)
			require.Equal(t, band, [2]int64{fast, rej}, "%s max_tokens=%s", model, o)
		}
		require.Equal(t, []string{model}, p.Models, "no alias expansion")
		require.NotEmpty(t, p.Evidence)
	}
}

// Exact byte bands from the reviewed calibration, and the comparisons at both edges.
func TestLongContextProfileBandsExactEdges(t *testing.T) {
	want := map[string]map[int64][2]int64{
		"claude-sonnet-4-6": {1024: {127614, 2108952}, 8192: {122604, 2032979}, 32000: {105965, 1780638}, 64000: {83600, 1441471}},
		"claude-opus-4-6":   {1024: {127613, 2107929}, 8192: {122604, 2031993}, 32000: {105964, 1779775}, 64000: {83599, 1440772}},
	}
	for model, bands := range want {
		p := findLongContextProfile(longContextRouteAnthropicOAuth, model)
		require.NotNil(t, p)
		for o, b := range bands {
			fast, rej := calibrationBands(p, o)
			require.Equal(t, b, [2]int64{fast, rej}, "%s %d", model, o)
			budget := longContextInputBudget(o)
			require.Equal(t, longContextAdmit, decideLongContextLocally(int(fast), budget, p))
			require.Equal(t, longContextUncertain, decideLongContextLocally(int(fast+1), budget, p))
			require.Equal(t, longContextReject, decideLongContextLocally(int(rej), budget, p))
			require.Equal(t, longContextUncertain, decideLongContextLocally(int(rej-1), budget, p))
		}
		// A 1.8 MB eligible body is counted at max_tokens 8192 but rejected locally at 32000.
		require.Equal(t, longContextUncertain, decideLongContextLocally(1_800_000, longContextInputBudget(8192), p))
		require.Equal(t, longContextReject, decideLongContextLocally(1_800_000, longContextInputBudget(32000), p))
	}
}

// The recorded lower-envelope limitation: highly compressible input (many_blocks-3) counts
// below the lower bound, so near the window it can be rejected although it would fit. At
// its own size and max_tokens 1024 it was not rejected (L is below the budget).
func TestLongContextProfileLowerEnvelopeLimitation(t *testing.T) {
	for model, c := range map[string]struct{ b, count, lower int64 }{
		"claude-sonnet-4-6": {763002, 40590, 63796},
		"claude-opus-4-6":   {763000, 40590, 63831},
	} {
		p := findLongContextProfile(longContextRouteAnthropicOAuth, model)
		lo, _ := calibrationInterval(p, c.b)
		require.Equal(t, c.lower, lo, model)
		require.Greater(t, lo, c.count, "L > C: the lower bound is not universal")
		require.Equal(t, longContextUncertain, decideLongContextLocally(int(c.b), longContextInputBudget(1024), p), "counted, not rejected")
	}
}

// Through Forward with the shipped registry: the two exact models decide locally for plain
// text; other models, routes and content are admitted without a bound, and nothing is counted.
func TestLongContextShippedProfilesInForward(t *testing.T) {
	apiKey := &Account{ID: 202, Name: "anthropic-apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
	small := func(model string) string { return strings.Replace(smallMessages, "claude-sonnet-4-6", model, 1) }
	for _, tc := range []struct {
		name     string
		account  *Account
		body     string
		decision string
	}{
		{"sonnet 4.6 small text", nil, small("claude-sonnet-4-6"), "profile_admit"},
		{"opus 4.6 small text", nil, small("claude-opus-4-6"), "profile_admit"},
		{"other model", nil, small("claude-sonnet-4-5"), "unprofiled_admit"},
		{"dated alias", nil, small("claude-sonnet-4-6-20260301"), "unprofiled_admit"},
		{"api key route", apiKey, small("claude-sonnet-4-6"), "unprofiled_admit"},
		{"image", nil, `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`, "ineligible_admit"},
		{"unknown field", nil, `{"model":"claude-sonnet-4-6","max_tokens":1024,"mcp_servers":[],"messages":[{"role":"user","content":"hi"}]}`, "ineligible_admit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := tc.account
			if account == nil {
				account = newAnthropicOAuthAccountForPartialUsageTest()
			}
			upstream := countOK("10")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, account, tc.body)
			require.NoError(t, err)
			require.Equal(t, 1, upstream.inferCalls)
			require.Zero(t, upstream.countCalls)
			require.Equal(t, tc.decision, observationOf(t, c).Decision)
		})
	}
	t.Run("huge plain text: local reject without any call", func(t *testing.T) {
		upstream := countOK("1")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		body := `{"model":"claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` + strings.Repeat("calibrated ", 200_000) + `"}]}`
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
		requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, 400)
		require.Equal(t, "profile_reject", observationOf(t, c).Decision)
		require.Zero(t, upstream.countCalls+upstream.inferCalls)
	})
}
