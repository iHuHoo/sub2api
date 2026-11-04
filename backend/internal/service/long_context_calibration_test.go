//go:build unit || calibration

package service

// Calibration harness for long context profiles (#139). It is test-only code: a frozen
// synthetic corpus, the coefficient fitting and held-out validation, and a budgeted,
// ledgered runner around an injected counter. The live counter lives in
// long_context_calibration_live_test.go (calibration tag) and is never run by CI.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	calibrationCorpusVersion = "lc-corpus-v1"
	calibrationMaxBodyBytes  = 2 << 20
	calibrationMaxAttempts   = 98
	calibrationMinInterval   = 10 * time.Second
	calibrationCallTimeout   = 10 * time.Second
)

var calibrationModels = []string{"claude-sonnet-4-6", "claude-opus-4-6"}

type calibrationFixture struct {
	Name      string `json:"name"`
	Set       string `json:"set"`  // fitting | heldout
	Kind      string `json:"kind"` // realistic | adversarial
	Generator string `json:"generator"`
	Messages  []byte `json:"-"` // the request body without model/max_tokens; those are added per model
	SHA256    string `json:"sha256"`
}

// calibrationCorpus is deterministic: same seeds, same bytes, same hashes. Fitting and
// held-out membership is fixed here, before any result exists.
func calibrationCorpus() []calibrationFixture {
	type spec struct {
		gen   string
		kind  string
		sizes []int // approximate content bytes; fitting uses even indexes, held-out odd
	}
	specs := []spec{
		{"prose", "realistic", []int{2_000, 60_000, 400_000, 700_000}},
		{"code", "realistic", []int{3_000, 80_000, 450_000, 650_000}},
		{"tool_json", "realistic", []int{4_000, 90_000, 300_000, 600_000}},
		{"cjk", "realistic", []int{2_000, 50_000, 250_000, 450_000}},
		{"tool_exchange", "realistic", []int{5_000, 100_000, 350_000, 750_000}},
		{"mixed_realistic", "realistic", []int{6_000, 120_000, 500_000, 1_200_000}},
		{"prose_large", "realistic", []int{900_000, 1_500_000, 1_100_000, 1_700_000}},
		{"code_large", "realistic", []int{1_000_000, 1_450_000, 1_200_000, 1_600_000}},
		{"repetition", "adversarial", []int{50_000, 600_000, 1_500_000, 300_000}},
		{"random_ids", "adversarial", []int{20_000, 200_000, 500_000, 900_000}},
		{"unicode_edge", "adversarial", []int{10_000, 150_000, 400_000, 700_000}},
		{"many_blocks", "adversarial", []int{30_000, 250_000, 600_000, 1_000_000}},
	}
	var out []calibrationFixture
	for _, sp := range specs {
		for i, size := range sp.sizes {
			set := "fitting"
			if i%2 == 1 {
				set = "heldout"
			}
			name := fmt.Sprintf("%s-%d", sp.gen, i)
			body := calibrationGenerate(sp.gen, size, int64(len(out)+1)*7919)
			sum := sha256.Sum256(body)
			out = append(out, calibrationFixture{Name: name, Set: set, Kind: sp.kind, Generator: sp.gen, Messages: body, SHA256: hex.EncodeToString(sum[:])})
		}
	}
	return out
}

var calibrationWords = strings.Fields(`the of and to in is that for it as with was on be by this are from or have an
which not at but can all were they one has their more will would there been other when what
system request response gateway account model token context window budget limit policy value
service handler stream error retry deploy release review change update configuration`)

func calibrationGenerate(gen string, size int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	text := func(n int, alphabet func() string) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(alphabet())
		}
		return b.String()
	}
	prose := func() string {
		w := calibrationWords[r.Intn(len(calibrationWords))]
		if r.Intn(12) == 0 {
			return w + ". "
		}
		return w + " "
	}
	code := func() string {
		id := calibrationWords[r.Intn(len(calibrationWords))]
		return fmt.Sprintf("func %s%d(ctx context.Context, n int) error {\n\tif n > %d {\n\t\treturn fmt.Errorf(\"%s: %%d\", n)\n\t}\n\treturn nil\n}\n\n", id, r.Intn(1000), r.Intn(100), id)
	}
	cjk := func() string {
		const s = "长上下文请求在发起推理之前需要按照实际长度进行准入判断，网关会估算输入大小并在必要时调用计数接口。"
		rs := []rune(s)
		return string(rs[r.Intn(len(rs)-8):][:8])
	}
	user := func(content string) map[string]any { return map[string]any{"role": "user", "content": content} }
	var msgs []any
	var tools []any
	switch gen {
	case "prose", "prose_large":
		msgs = []any{user(text(size, prose))}
	case "code", "code_large":
		msgs = []any{user(text(size, code))}
	case "cjk":
		msgs = []any{user(text(size, cjk))}
	case "tool_json":
		for len(tools)*400 < size/2 {
			tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%d", len(tools)), "description": text(200, prose),
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": text(80, prose)}, "limit": map[string]any{"type": "integer"}}, "required": []string{"query"}}})
		}
		msgs = []any{user(text(size/2, prose))}
	case "tool_exchange", "mixed_realistic":
		tools = []any{map[string]any{"name": "read_file", "description": "Read a file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
		msgs = []any{user("Please inspect the repository.")}
		for i, n := 0, 0; n < size; i++ {
			result := text(4_000+r.Intn(8_000), code)
			if gen == "mixed_realistic" && i%2 == 1 {
				result = text(3_000, prose) + text(1_000, cjk)
			}
			id := fmt.Sprintf("toolu_%04d", i)
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text(200, prose)}, map[string]any{"type": "tool_use", "id": id, "name": "read_file", "input": map[string]any{"path": fmt.Sprintf("pkg/file_%d.go", i)}}}},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result}}})
			n += len(result) + 300
		}
		msgs = append(msgs, user("Summarize."))
	case "repetition":
		msgs = []any{user(strings.Repeat("a", size/2) + strings.Repeat(" ", size/4) + strings.Repeat("ab", size/8))}
	case "random_ids":
		const hexd = "0123456789abcdef"
		msgs = []any{user(text(size, func() string {
			b := make([]byte, 33)
			for i := range b[:32] {
				b[i] = hexd[r.Intn(16)]
			}
			b[32] = '\n'
			return string(b)
		}))}
	case "unicode_edge":
		parts := []string{"😀", "👩🏽\u200d💻", "e\u0301", "\u200d", "\\u0000", "\"", "\t", "𝔘𝔫𝔦", "ﷺ", "🇸🇬"}
		msgs = []any{user(text(size, func() string { return parts[r.Intn(len(parts))] }))}
	case "many_blocks":
		var blocks []any
		for n := 0; n < size; n += 40 {
			blocks = append(blocks, map[string]any{"type": "text", "text": calibrationWords[r.Intn(len(calibrationWords))]})
		}
		msgs = []any{map[string]any{"role": "user", "content": blocks}}
	default:
		panic("unknown generator " + gen)
	}
	req := map[string]any{"messages": msgs}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	b, err := json.Marshal(req)
	if err != nil {
		panic(err)
	}
	return b
}

// calibrationRequestBody adds the per-model fields to a fixture.
func calibrationRequestBody(f calibrationFixture, model string) []byte {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(f.Messages, &m)
	m["model"], _ = json.Marshal(model)
	m["max_tokens"] = json.RawMessage("1024")
	b, _ := json.Marshal(m)
	return b
}

// calibrationSample is one counted fixture: B is the final prepared inference body length.
type calibrationSample struct {
	Fixture string
	Set     string
	Kind    string
	B       int64
	C       int64
}

type calibrationResult struct {
	Profile         *longContextProfile
	Violations      []string // held-out cases that reject the candidate
	FalseRejections []string // adversarial held-out cases with L > C (recorded, not a failure)
}

// fitCalibrationProfile derives a_low = q_min_real/2 (realistic fitting only) and
// a_high = 2*q_max_all (all fitting), then validates every held-out sample.
func fitCalibrationProfile(name string, route longContextRoute, model string, samples []calibrationSample) (calibrationResult, error) {
	var minReal, maxAll *big.Rat
	for _, s := range samples {
		if s.Set != "fitting" || s.B <= 0 {
			continue
		}
		q := big.NewRat(s.C, s.B)
		if maxAll == nil || q.Cmp(maxAll) > 0 {
			maxAll = q
		}
		if s.Kind == "realistic" && (minReal == nil || q.Cmp(minReal) < 0) {
			minReal = q
		}
	}
	if minReal == nil || maxAll == nil {
		return calibrationResult{}, errors.New("not enough fitting samples")
	}
	low := new(big.Rat).Quo(minReal, big.NewRat(2, 1))
	high := new(big.Rat).Mul(maxAll, big.NewRat(2, 1))
	p := &longContextProfile{
		Name: name, Route: route, Models: []string{model},
		LowNum: low.Num().Int64(), LowDen: low.Denom().Int64(),
		HighNum: high.Num().Int64(), HighDen: high.Denom().Int64(),
	}
	res := calibrationResult{Profile: p}
	for _, s := range samples {
		if s.Set != "heldout" {
			continue
		}
		lo, up := calibrationInterval(p, s.B)
		if s.C > up {
			res.Violations = append(res.Violations, fmt.Sprintf("%s: C=%d > U=%d", s.Fixture, s.C, up))
		}
		if s.C < lo {
			if s.Kind == "realistic" {
				res.Violations = append(res.Violations, fmt.Sprintf("%s: C=%d < L=%d", s.Fixture, s.C, lo))
			} else {
				res.FalseRejections = append(res.FalseRejections, fmt.Sprintf("%s: C=%d < L=%d", s.Fixture, s.C, lo))
			}
		}
	}
	return res, nil
}

// calibrationInterval returns L and U exactly as decideLongContextLocally computes them.
func calibrationInterval(p *longContextProfile, b int64) (lo, up int64) {
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

// calibrationBands reports, for a max_tokens value, the largest body admitted locally and
// the smallest body rejected locally (exact comparisons in decideLongContextLocally govern).
func calibrationBands(p *longContextProfile, maxTokens int64) (fastPassMax, rejectMin int64) {
	t := longContextInputBudget(maxTokens)
	// U <= T  <=>  ceil(B*hn/hd) <= T-H  <=>  B <= floor((T-H)*hd/hn)
	fastPassMax = (t - longContextFramingAllowance) * p.HighDen / p.HighNum
	// L > T  <=>  floor(B*ln/ld) > T+H  <=>  B >= ceil((T+H+1)*ld/ln)
	if p.LowNum > 0 {
		rejectMin = ((t+longContextFramingAllowance+1)*p.LowDen + p.LowNum - 1) / p.LowNum
	}
	return
}

// --- budgeted, ledgered runner -------------------------------------------------------

type calibrationLedgerEntry struct {
	Seq      int       `json:"seq"`
	Phase    string    `json:"phase"` // dispatch | result
	Model    string    `json:"model"`
	Fixture  string    `json:"fixture"`
	At       time.Time `json:"at"`
	BodyB    int64     `json:"body_bytes,omitempty"`
	Status   int       `json:"status,omitempty"`
	Tokens   int64     `json:"input_tokens,omitempty"`
	ErrClass string    `json:"error_class,omitempty"`
}

// calibrationCounter performs exactly one count call. It must not retry. transport
// reports a network-level failure (no HTTP status).
type calibrationCounter func(model string, body []byte) (tokens int64, status int, transport bool, err error)

type calibrationRunner struct {
	LedgerPath string
	Counter    calibrationCounter
	Now        func() time.Time
	Sleep      func(time.Duration)
}

var errCalibrationStopped = errors.New("calibration stopped")

// readCalibrationLedger returns the dispatched attempt count and last dispatch time, so an
// interrupted run can never reset or exceed the shared budget.
func readCalibrationLedger(path string) (dispatched int, last time.Time, results map[string]calibrationLedgerEntry, err error) {
	results = map[string]calibrationLedgerEntry{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, time.Time{}, results, nil
	}
	if err != nil {
		return 0, time.Time{}, nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e calibrationLedgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return 0, time.Time{}, nil, fmt.Errorf("corrupt ledger: %w", err)
		}
		if e.Phase == "dispatch" {
			dispatched++
			last = e.At
		} else {
			results[e.Model+"/"+e.Fixture] = e
		}
	}
	return dispatched, last, results, sc.Err()
}

func appendCalibrationLedger(path string, e calibrationLedgerEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(e)
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// run dispatches the given (model, fixture, body) jobs in order, skipping any that already
// have a successful result. It stops on budget exhaustion, 401/403/429, or two consecutive
// transport failures. A dispatch is recorded before the call is made.
func (r *calibrationRunner) run(jobs []calibrationJob) error {
	dispatched, last, results, err := readCalibrationLedger(r.LedgerPath)
	if err != nil {
		return err
	}
	consecutiveTransport := 0
	for _, j := range jobs {
		if prev, ok := results[j.Model+"/"+j.Fixture]; ok && prev.ErrClass == "" {
			continue
		}
		if dispatched >= calibrationMaxAttempts {
			return fmt.Errorf("%w: attempt budget %d exhausted", errCalibrationStopped, calibrationMaxAttempts)
		}
		if len(j.Body) > calibrationMaxBodyBytes {
			return fmt.Errorf("%w: %s body %d bytes exceeds cap", errCalibrationStopped, j.Fixture, len(j.Body))
		}
		if !last.IsZero() {
			if wait := calibrationMinInterval - r.Now().Sub(last); wait > 0 {
				r.Sleep(wait)
			}
		}
		dispatched++
		last = r.Now()
		if err := appendCalibrationLedger(r.LedgerPath, calibrationLedgerEntry{Seq: dispatched, Phase: "dispatch", Model: j.Model, Fixture: j.Fixture, At: last, BodyB: int64(len(j.Body))}); err != nil {
			return err
		}
		tokens, status, transport, callErr := r.Counter(j.Model, j.Body)
		entry := calibrationLedgerEntry{Seq: dispatched, Phase: "result", Model: j.Model, Fixture: j.Fixture, At: r.Now(), BodyB: int64(len(j.Body)), Status: status, Tokens: tokens}
		switch {
		case callErr == nil && status == 200:
			consecutiveTransport = 0
		case transport:
			entry.ErrClass = "transport"
			consecutiveTransport++
		default:
			entry.ErrClass = fmt.Sprintf("status_%d", status)
			consecutiveTransport = 0
		}
		if err := appendCalibrationLedger(r.LedgerPath, entry); err != nil {
			return err
		}
		if status == 401 || status == 403 || status == 429 {
			return fmt.Errorf("%w: %s/%s returned %d", errCalibrationStopped, j.Model, j.Fixture, status)
		}
		if consecutiveTransport >= 2 {
			return fmt.Errorf("%w: two consecutive transport failures", errCalibrationStopped)
		}
		if j.Readiness && entry.ErrClass != "" {
			return fmt.Errorf("%w: readiness call for %s failed (%s)", errCalibrationStopped, j.Model, entry.ErrClass)
		}
	}
	return nil
}

type calibrationJob struct {
	Model     string
	Fixture   string
	Body      []byte
	Readiness bool
}

// --- tests -------------------------------------------------------------------------

func TestCalibrationCorpusIsFrozenAndBalanced(t *testing.T) {
	a, b := calibrationCorpus(), calibrationCorpus()
	require.Len(t, a, 48)
	counts := map[string]int{}
	for i := range a {
		require.Equal(t, a[i].SHA256, b[i].SHA256, "deterministic")
		counts[a[i].Set+"/"+a[i].Kind]++
		for _, m := range calibrationModels {
			require.LessOrEqual(t, len(calibrationRequestBody(a[i], m)), calibrationMaxBodyBytes, a[i].Name)
		}
	}
	require.Equal(t, map[string]int{"fitting/realistic": 16, "fitting/adversarial": 8, "heldout/realistic": 16, "heldout/adversarial": 8}, counts)
}

func TestFitCalibrationProfile(t *testing.T) {
	samples := []calibrationSample{
		{"r1", "fitting", "realistic", 1000, 250},
		{"r2", "fitting", "realistic", 1000, 300},
		{"a1", "fitting", "adversarial", 1000, 50},
		{"a2", "fitting", "adversarial", 1000, 400},
		{"h1", "heldout", "realistic", 100_000, 27_000},
		{"h2", "heldout", "adversarial", 100_000, 1_000},
	}
	res, err := fitCalibrationProfile("p", longContextRouteAnthropicOAuth, "m", samples)
	require.NoError(t, err)
	// a_low = 0.25/2 = 1/8 (adversarial fitting does not lower it); a_high = 0.4*2 = 4/5.
	require.Equal(t, int64(1), res.Profile.LowNum)
	require.Equal(t, int64(8), res.Profile.LowDen)
	require.Equal(t, int64(4), res.Profile.HighNum)
	require.Equal(t, int64(5), res.Profile.HighDen)
	require.Empty(t, res.Violations)
	require.Len(t, res.FalseRejections, 1, "compressible adversarial input is a recorded false rejection")

	bad := append(samples, calibrationSample{"h3", "heldout", "realistic", 1000, 9_999})
	res, err = fitCalibrationProfile("p", longContextRouteAnthropicOAuth, "m", bad)
	require.NoError(t, err)
	require.Len(t, res.Violations, 1, "an upper-bound violation rejects the candidate")

	lo, up := calibrationInterval(res.Profile, 100_000)
	require.Equal(t, longContextAdmit == decideLongContextLocally(100_000, up, res.Profile), true)
	require.Equal(t, longContextReject, decideLongContextLocally(100_000, lo-1, res.Profile))
	fast, rej := calibrationBands(res.Profile, 1024)
	require.Equal(t, longContextAdmit, decideLongContextLocally(int(fast), longContextInputBudget(1024), res.Profile))
	require.NotEqual(t, longContextAdmit, decideLongContextLocally(int(fast+1), longContextInputBudget(1024), res.Profile))
	require.Equal(t, longContextReject, decideLongContextLocally(int(rej), longContextInputBudget(1024), res.Profile))
	require.NotEqual(t, longContextReject, decideLongContextLocally(int(rej-1), longContextInputBudget(1024), res.Profile))
}

func TestCalibrationRunnerBudgetLedgerAndStops(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	var slept time.Duration
	sleep := func(d time.Duration) { slept += d; now = now.Add(d) }
	jobs := func(n int) []calibrationJob {
		var js []calibrationJob
		for i := 0; i < n; i++ {
			js = append(js, calibrationJob{Model: "m", Fixture: fmt.Sprintf("f%d", i), Body: []byte("{}")})
		}
		return js
	}

	t.Run("budget survives a restart and pacing is enforced", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		calls := 0
		ok := func(string, []byte) (int64, int, bool, error) { calls++; return 10, 200, false, nil }
		r := &calibrationRunner{LedgerPath: ledger, Counter: ok, Now: clock, Sleep: sleep}
		require.NoError(t, r.run(jobs(60)))
		require.Equal(t, 59*calibrationMinInterval, slept)
		// Resume with a new job list: completed fixtures are skipped, total never exceeds 98.
		more := jobs(60)
		for i := range more {
			more[i].Fixture = "g" + more[i].Fixture
		}
		err := (&calibrationRunner{LedgerPath: ledger, Counter: ok, Now: clock, Sleep: sleep}).run(append(jobs(60), more...))
		require.ErrorIs(t, err, errCalibrationStopped)
		require.Equal(t, calibrationMaxAttempts, calls)
		n, _, _, err := readCalibrationLedger(ledger)
		require.NoError(t, err)
		require.Equal(t, calibrationMaxAttempts, n)
	})
	for _, tc := range []struct {
		name    string
		counter calibrationCounter
		calls   int
	}{
		{"stops on 429", func(string, []byte) (int64, int, bool, error) { return 0, 429, false, errors.New("x") }, 1},
		{"stops on 401", func(string, []byte) (int64, int, bool, error) { return 0, 401, false, errors.New("x") }, 1},
		{"stops after two transport failures", func(string, []byte) (int64, int, bool, error) { return 0, 0, true, errors.New("x") }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			counter := func(m string, b []byte) (int64, int, bool, error) { calls++; return tc.counter(m, b) }
			err := (&calibrationRunner{LedgerPath: t.TempDir() + "/l.jsonl", Counter: counter, Now: clock, Sleep: sleep}).run(jobs(5))
			require.ErrorIs(t, err, errCalibrationStopped)
			require.Equal(t, tc.calls, calls)
		})
	}
	t.Run("failed readiness stops", func(t *testing.T) {
		calls := 0
		js := jobs(3)
		js[0].Readiness = true
		err := (&calibrationRunner{LedgerPath: t.TempDir() + "/l.jsonl", Counter: func(string, []byte) (int64, int, bool, error) {
			calls++
			return 0, 500, false, errors.New("x")
		}, Now: clock, Sleep: sleep}).run(js)
		require.ErrorIs(t, err, errCalibrationStopped)
		require.Equal(t, 1, calls)
	})
}
