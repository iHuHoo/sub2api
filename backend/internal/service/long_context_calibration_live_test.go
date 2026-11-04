//go:build calibration

package service

// Live calibration entry point for #139. Never part of CI (calibration build tag only).
//
// Dry run (no network): prepares every fixture through the real Forward preparation and
// prints the final body sizes.
//
//	LONGCTX_CALIBRATION=dry go test -tags=calibration ./internal/service -run TestLongContextCalibrationLive -v
//
// Live run (authorized budget only): additionally needs
//
//	LONGCTX_CALIBRATION=live
//	LONGCTX_OUT=<absolute dir>          ledger.jsonl, samples.json, profiles.json are written here
//	LONGCTX_OAUTH_TOKEN_FILE=<file>     OAuth access token of the approved account; never logged
//	LONGCTX_PROXY_URL_FILE=<file>       optional: the account's approved proxy URL
//
// Each count call is checked to go to api.anthropic.com with a bearer credential before it
// is dispatched. The runner enforces the shared attempt budget, pacing and stop rules.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// captureUpstream records the inference body Forward would send and answers locally.
type captureUpstream struct{ body []byte }

func (u *captureUpstream) Do(req *http.Request, p string, id int64, c int) (*http.Response, error) {
	return u.DoWithTLS(req, p, id, c, nil)
}

func (u *captureUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.body, _ = io.ReadAll(req.Body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(
		`{"id":"msg_cal","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func calibrationAccount() *Account {
	return &Account{ID: 71, Name: "calibration", Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "placeholder"}, Status: StatusActive, Schedulable: true}
}

// prepareCalibrationWireBody runs the real Messages preparation (OAuth mimicry included)
// and returns the exact inference body, which is what B measures and what is counted.
func prepareCalibrationWireBody(t *testing.T, body []byte) []byte {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	up := &captureUpstream{}
	svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: up,
		rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}, tlsFPProfileService: &TLSFingerprintProfileService{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(context.Background(), c, calibrationAccount(), parsed)
	require.NoError(t, err)
	require.NotEmpty(t, up.body)
	return up.body
}

func TestLongContextCalibrationLive(t *testing.T) {
	mode := os.Getenv("LONGCTX_CALIBRATION")
	if mode != "dry" && mode != "live" {
		t.Skip("set LONGCTX_CALIBRATION=dry or live")
	}
	corpus := calibrationCorpus()
	type prepared struct {
		f    calibrationFixture
		wire []byte
	}
	byModel := map[string][]prepared{}
	var jobs []calibrationJob
	for _, model := range calibrationModels {
		ready := prepareCalibrationWireBody(t, []byte(`{"model":"`+model+`","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`))
		jobs = append(jobs, calibrationJob{Model: model, Fixture: "readiness", Body: ready, Readiness: true})
		for _, f := range corpus {
			wire := prepareCalibrationWireBody(t, calibrationRequestBody(f, model))
			require.LessOrEqual(t, len(wire), calibrationMaxBodyBytes, f.Name)
			byModel[model] = append(byModel[model], prepared{f, wire})
			jobs = append(jobs, calibrationJob{Model: model, Fixture: f.Name, Body: wire})
			t.Logf("%s %-20s %-8s %-11s B=%d sha256=%s", model, f.Name, f.Set, f.Kind, len(wire), f.SHA256[:12])
		}
	}
	require.LessOrEqual(t, len(jobs), calibrationMaxAttempts)
	if mode == "dry" {
		return
	}

	out := os.Getenv("LONGCTX_OUT")
	require.True(t, filepath.IsAbs(out), "LONGCTX_OUT must be absolute")
	tokenBytes, err := os.ReadFile(os.Getenv("LONGCTX_OAUTH_TOKEN_FILE"))
	require.NoError(t, err)
	token := strings.TrimSpace(string(tokenBytes))
	require.NotEmpty(t, token)
	transport := &http.Transport{}
	if p := os.Getenv("LONGCTX_PROXY_URL_FILE"); p != "" {
		raw, err := os.ReadFile(p)
		require.NoError(t, err)
		proxy, err := url.Parse(strings.TrimSpace(string(raw)))
		require.NoError(t, err)
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport, Timeout: calibrationCallTimeout}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{cfg: cfg, tlsFPProfileService: &TLSFingerprintProfileService{}}
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	buildCount := func(model string, wire []byte) (*http.Request, error) {
		// client.Timeout bounds each call to calibrationCallTimeout.
		req, _, err := svc.buildCountTokensRequest(context.Background(), gc, calibrationAccount(), append([]byte(nil), wire...), token, "oauth", model, true)
		if err != nil {
			return nil, err
		}
		if req.URL.Host != "api.anthropic.com" || !strings.HasPrefix(req.Header.Get("authorization"), "Bearer ") || req.Header.Get("x-api-key") != "" {
			return nil, errors.New("unexpected destination or credential pairing")
		}
		return req, nil
	}
	// Verify the pairing for every job before the first dispatch.
	for _, j := range jobs {
		_, err := buildCount(j.Model, j.Body)
		require.NoError(t, err)
	}
	counter := func(model string, body []byte) (int64, int, bool, error) {
		req, err := buildCount(model, body)
		if err != nil {
			return 0, 0, false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, true, err
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, longContextCountMaxResponseBytes))
		if resp.StatusCode != 200 {
			return 0, resp.StatusCode, false, errors.New("count status")
		}
		v := gjson.GetBytes(data, "input_tokens")
		if v.Type != gjson.Number || v.Int() < 0 {
			return 0, resp.StatusCode, false, errors.New("invalid count")
		}
		return v.Int(), 200, false, nil
	}
	ledger := filepath.Join(out, "ledger.jsonl")
	runErr := (&calibrationRunner{LedgerPath: ledger, Counter: counter, Now: time.Now, Sleep: time.Sleep}).run(jobs)

	_, _, results, err := readCalibrationLedger(ledger)
	require.NoError(t, err)
	report := map[string]any{}
	for _, model := range calibrationModels {
		var samples []calibrationSample
		for _, p := range byModel[model] {
			if r, ok := results[model+"/"+p.f.Name]; ok && r.ErrClass == "" {
				samples = append(samples, calibrationSample{Fixture: p.f.Name, Set: p.f.Set, Kind: p.f.Kind, B: int64(len(p.wire)), C: r.Tokens})
			}
		}
		entry := map[string]any{"samples": samples, "corpus_version": calibrationCorpusVersion}
		if res, err := fitCalibrationProfile("anthropic-oauth-"+model+"-"+calibrationCorpusVersion, longContextRouteAnthropicOAuth, model, samples); err == nil {
			bands := map[int64][2]int64{}
			for _, o := range []int64{1024, 8192, 32000, 64000} {
				f, r := calibrationBands(res.Profile, o)
				bands[o] = [2]int64{f, r}
			}
			entry["profile"], entry["violations"], entry["false_rejections"], entry["bands_fastpass_max_reject_min"] = res.Profile, res.Violations, res.FalseRejections, bands
		} else {
			entry["fit_error"] = err.Error()
		}
		report[model] = entry
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(out, "profiles.json"), data, 0o600))
	require.NoError(t, runErr)
}
