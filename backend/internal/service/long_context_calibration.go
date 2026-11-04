//go:build unit || calibration

package service

// Calibration harness for long context profiles (#139): a frozen synthetic corpus, the
// coefficient fitting and held-out validation, a durable budgeted ledger, and a calibrator
// that uses the gateway's own preparation, count request builder and HTTP upstream.
// It is compiled only for tests and for cmd/longctx-calibrate (calibration tag); production
// builds never include it.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
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
	Fixture string `json:"fixture"`
	Set     string `json:"set"`
	Kind    string `json:"kind"`
	B       int64  `json:"body_bytes"`
	C       int64  `json:"input_tokens"`
}

type calibrationResult struct {
	Profile         *longContextProfile `json:"profile"`
	Violations      []string            `json:"violations"`       // held-out cases that reject the candidate
	FalseRejections []string            `json:"false_rejections"` // adversarial held-out cases with L > C (recorded, not a failure)
}

// calibrationExpectedCounts is the frozen corpus split; a fit on fewer samples is refused.
var calibrationExpectedCounts = map[string]int{"fitting/realistic": 16, "fitting/adversarial": 8, "heldout/realistic": 16, "heldout/adversarial": 8}

// fitCalibrationProfile derives a_low = q_min_real/2 (realistic fitting only) and
// a_high = 2*q_max_all (all fitting), then validates every held-out sample. It refuses an
// incomplete sample set: a profile is never fitted on whatever happened to succeed.
func fitCalibrationProfile(name string, route longContextRoute, model string, samples []calibrationSample, want map[string]int) (calibrationResult, error) {
	got := map[string]int{}
	for _, s := range samples {
		got[s.Set+"/"+s.Kind]++
	}
	for k, n := range want {
		if got[k] != n {
			return calibrationResult{}, fmt.Errorf("incomplete samples: %s has %d of %d", k, got[k], n)
		}
	}
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
	if minReal == nil || maxAll == nil || maxAll.Sign() <= 0 {
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

// calibrationInterval returns L and U exactly as admission computes them.
func calibrationInterval(p *longContextProfile, b int64) (lo, up int64) {
	return longContextInterval(p, b)
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

// --- durable ledger and runner -------------------------------------------------------

type calibrationJob struct {
	Model     string
	Fixture   string
	Set       string
	Kind      string
	Body      []byte // exact prepared inference body (B = len)
	Readiness bool
}

func (j calibrationJob) key() string { return j.Model + "/" + j.Fixture }

func (j calibrationJob) bodySHA() string {
	sum := sha256.Sum256(j.Body)
	return hex.EncodeToString(sum[:])
}

// calibrationManifest binds a ledger to the exact jobs, account and budget.
func calibrationManifest(accountID int64, jobs []calibrationJob) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "corpus=%s\naccount=%d\nbudget=%d\n", calibrationCorpusVersion, accountID, calibrationMaxAttempts)
	for _, j := range jobs {
		_, _ = fmt.Fprintf(h, "%s\t%s\t%t\n", j.key(), j.bodySHA(), j.Readiness)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type calibrationLedgerEntry struct {
	Seq       int                       `json:"seq,omitempty"`
	Phase     string                    `json:"phase"` // manifest | dispatch | result | stop | resume
	Manifest  string                    `json:"manifest_sha256,omitempty"`
	AccountID int64                     `json:"account_id,omitempty"`
	Budget    int                       `json:"budget,omitempty"`
	Job       string                    `json:"job,omitempty"`
	BodySHA   string                    `json:"body_sha256,omitempty"`
	At        time.Time                 `json:"at"`
	BodyB     int64                     `json:"body_bytes,omitempty"`
	Status    int                       `json:"status,omitempty"`
	Tokens    int64                     `json:"input_tokens,omitempty"`
	ErrClass  string                    `json:"error_class,omitempty"`
	Reason    string                    `json:"reason,omitempty"`
	Response  *calibrationErrorResponse `json:"response,omitempty"`
	// Terminal is set on the result itself, so a stop is durable even if the separate stop
	// row is never written (crash, write error).
	Terminal string `json:"terminal,omitempty"`
	// resume only: the manifest it replaces, the build revision and the recorded approval.
	PrevManifest string `json:"prev_manifest_sha256,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Approval     string `json:"approval,omitempty"`
}

type calibrationLedgerState struct {
	manifest          string
	dispatched        int
	last              time.Time
	results           map[string]calibrationLedgerEntry
	pending           map[string]bool // dispatched, no result: outcome unknown
	stopped           string
	trailingTransport int
	resumed           bool
	preResume         map[string]bool // results recorded before the resume: kept as evidence, never terminal again
}

// readCalibrationLedger restores everything a resumed run needs: the bound manifest, the
// attempt count, terminal stops, unresolved dispatches and the transport failure streak.
func readCalibrationLedger(path string) (*calibrationLedgerState, error) {
	st := &calibrationLedgerState{results: map[string]calibrationLedgerEntry{}, pending: map[string]bool{}, preResume: map[string]bool{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		var e calibrationLedgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("corrupt ledger: %w", err)
		}
		if first != (e.Phase == "manifest") {
			return nil, errors.New("corrupt ledger: manifest must be the first and only manifest entry")
		}
		first = false
		switch e.Phase {
		case "manifest":
			st.manifest = e.Manifest
		case "dispatch":
			st.dispatched++
			st.last = e.At
			st.pending[e.Job] = true
		case "result":
			if !st.pending[e.Job] {
				return nil, fmt.Errorf("corrupt ledger: result without dispatch for %s", e.Job)
			}
			delete(st.pending, e.Job)
			st.results[e.Job] = e
			if e.ErrClass == "transport" {
				st.trailingTransport++
			} else {
				st.trailingTransport = 0
			}
			if e.Terminal != "" && st.stopped == "" {
				st.stopped = e.Terminal
			}
		case "stop":
			st.stopped = e.Reason
		case "resume":
			// The one approved continuation: it rebinds the manifest and supersedes only the
			// stops recorded before it. Attempts and evidence carry forward unchanged.
			if st.resumed || e.PrevManifest != st.manifest || e.Manifest == "" || len(st.pending) > 0 {
				return nil, errors.New("corrupt ledger: invalid or repeated resume entry")
			}
			st.resumed = true
			st.manifest = e.Manifest
			st.stopped = ""
			st.trailingTransport = 0
			for job := range st.results {
				st.preResume[job] = true
			}
		default:
			return nil, fmt.Errorf("corrupt ledger: unknown phase %q", e.Phase)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if st.stopped == "" && st.trailingTransport >= 2 {
		st.stopped = "two consecutive transport failures"
	}
	return st, nil
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

// calibrationCounter performs exactly one count call and never retries. transport reports a
// network-level failure (no HTTP status).
type calibrationCounter func(job calibrationJob) (tokens int64, status int, transport bool, err error)

type calibrationRunner struct {
	LedgerPath string
	AccountID  int64
	Counter    calibrationCounter
	Now        func() time.Time
	Sleep      func(time.Duration)
}

var errCalibrationStopped = errors.New("calibration stopped")

// run dispatches jobs in order under the shared budget. Every job is attempted at most once
// across all invocations; a hard stop is durable; an unresolved dispatch or a different
// manifest refuses to run; one runner holds the ledger at a time.
func (r *calibrationRunner) run(jobs []calibrationJob) error {
	lock := r.LedgerPath + ".lock"
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: ledger is locked (%s); remove it only after confirming no runner is active", errCalibrationStopped, lock)
	}
	_ = lf.Close()
	defer func() { _ = os.Remove(lock) }()

	st, err := readCalibrationLedger(r.LedgerPath)
	if err != nil {
		return fmt.Errorf("%w: %v", errCalibrationStopped, err)
	}
	manifest := calibrationManifest(r.AccountID, jobs)
	switch {
	case st.manifest == "":
		if err := appendCalibrationLedger(r.LedgerPath, calibrationLedgerEntry{Phase: "manifest", Manifest: manifest, AccountID: r.AccountID, Budget: calibrationMaxAttempts, At: r.Now()}); err != nil {
			return err
		}
	case st.manifest != manifest:
		return fmt.Errorf("%w: ledger belongs to a different manifest", errCalibrationStopped)
	}
	if len(st.pending) > 0 {
		return fmt.Errorf("%w: %d dispatched attempt(s) have no recorded outcome; not retried", errCalibrationStopped, len(st.pending))
	}
	// A persisted result is terminal by its own content, whether or not its stop row (or
	// the terminal field) made it to disk.
	readiness := map[string]bool{}
	for _, j := range jobs {
		readiness[j.key()] = j.Readiness
	}
	for job, e := range st.results {
		if st.preResume[job] {
			continue
		}
		if reason := calibrationTerminalReason(e, readiness[job]); reason != "" && st.stopped == "" {
			st.stopped = reason
		}
	}
	if st.stopped != "" {
		return fmt.Errorf("%w: previously stopped: %s", errCalibrationStopped, st.stopped)
	}
	stop := func(reason string) error {
		if err := appendCalibrationLedger(r.LedgerPath, calibrationLedgerEntry{Phase: "stop", Reason: reason, At: r.Now()}); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", errCalibrationStopped, reason)
	}
	consecutiveTransport := st.trailingTransport
	dispatched, last := st.dispatched, st.last
	for _, j := range jobs {
		if _, done := st.results[j.key()]; done {
			continue // attempted before (success or failure): never retried
		}
		if dispatched >= calibrationMaxAttempts {
			return stop(fmt.Sprintf("attempt budget %d exhausted", calibrationMaxAttempts))
		}
		if len(j.Body) > calibrationMaxBodyBytes {
			return stop(fmt.Sprintf("%s body %d bytes exceeds cap", j.key(), len(j.Body)))
		}
		if !last.IsZero() {
			if wait := calibrationMinInterval - r.Now().Sub(last); wait > 0 {
				r.Sleep(wait)
			}
		}
		dispatched++
		last = r.Now()
		if err := appendCalibrationLedger(r.LedgerPath, calibrationLedgerEntry{Seq: dispatched, Phase: "dispatch", Job: j.key(), BodySHA: j.bodySHA(), At: last, BodyB: int64(len(j.Body))}); err != nil {
			return err
		}
		tokens, status, transport, callErr := r.Counter(j)
		entry := calibrationLedgerEntry{Seq: dispatched, Phase: "result", Job: j.key(), BodySHA: j.bodySHA(), At: r.Now(), BodyB: int64(len(j.Body)), Status: status, Tokens: tokens}
		switch {
		case callErr == nil && status == http.StatusOK:
			consecutiveTransport = 0
		case transport:
			entry.ErrClass = "transport"
			entry.Tokens = 0
			consecutiveTransport++
		default:
			entry.ErrClass = fmt.Sprintf("status_%d", status)
			entry.Tokens = 0
			consecutiveTransport = 0
		}
		entry.Terminal = calibrationTerminalReason(entry, j.Readiness)
		var responseErr *calibrationErrorResponse
		if errors.As(callErr, &responseErr) {
			entry.Response = responseErr
		}
		if entry.Terminal == "" && consecutiveTransport >= 2 {
			entry.Terminal = "two consecutive transport failures"
		}
		if entry.Terminal != "" && callErr != nil && entry.ErrClass == "status_0" {
			entry.Terminal += fmt.Sprintf(": %v", callErr)
		}
		if err := appendCalibrationLedger(r.LedgerPath, entry); err != nil {
			return err
		}
		if entry.Terminal != "" {
			return stop(entry.Terminal)
		}
	}
	return nil
}

// calibrationTerminalReason says whether a recorded result ends the experiment: an
// authentication or rate limit answer, a redirect, a refusal before sending (status_0),
// or any failed readiness call.
func calibrationTerminalReason(e calibrationLedgerEntry, readiness bool) string {
	switch {
	case e.Terminal != "":
		return e.Terminal
	case e.Status == 401 || e.Status == 403 || e.Status == 429 || (e.Status >= 300 && e.Status < 400):
		return fmt.Sprintf("%s returned %d", e.Job, e.Status)
	case e.ErrClass == "status_0":
		return fmt.Sprintf("%s refused before sending", e.Job)
	case readiness && e.ErrClass != "":
		return fmt.Sprintf("readiness call %s failed (%s)", e.Job, e.ErrClass)
	}
	return ""
}

// Counter responses are tiny; never buffer an unbounded body.
const longContextCountMaxResponseBytes = 64 << 10

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

// checkCalibrationCountRequest is the destination/credential guard applied to every count
// request before it is sent. Headers are stored in wire casing, so lookups are raw.
func checkCalibrationCountRequest(req *http.Request) error {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "api.anthropic.com" || req.URL.Path != "/v1/messages/count_tokens" {
		return errors.New("unexpected count destination")
	}
	if !strings.HasPrefix(getHeaderRaw(req.Header, "authorization"), "Bearer ") || getHeaderRaw(req.Header, "x-api-key") != "" {
		return errors.New("unexpected credential pairing")
	}
	return nil
}

// --- calibrator: the real preparation and counter, for cmd/longctx-calibrate ----------

// LongContextCalibrator prepares bodies with the gateway's own Messages preparation and
// counts them with the gateway's own count request builder and HTTP upstream.
type LongContextCalibrator struct {
	cfg      *config.Config
	upstream HTTPUpstream
	settings *SettingService
	tls      *TLSFingerprintProfileService
	identity *IdentityService // nil only in tests
	idCache  *ReadOnlyIdentityCache
}

// NewLongContextCalibrator wires the gateway's own dependencies. idCache wraps the
// production identity cache read-only; nil (tests only) skips identity rewriting.
func NewLongContextCalibrator(cfg *config.Config, upstream HTTPUpstream, settings *SettingService, tls *TLSFingerprintProfileService, idCache *ReadOnlyIdentityCache) *LongContextCalibrator {
	k := &LongContextCalibrator{cfg: cfg, upstream: upstream, settings: settings, tls: tls, idCache: idCache}
	if idCache != nil && settings != nil { // identity rewriting reads gateway settings
		k.identity = NewIdentityService(idCache)
	}
	return k
}

// ReadOnlyIdentityCache lets calibration use the account's production fingerprint and
// masked session exactly as the gateway would, without ever writing them.
type ReadOnlyIdentityCache struct {
	inner      IdentityCache
	missing    bool
	readFailed bool
}

func NewReadOnlyIdentityCache(inner IdentityCache) *ReadOnlyIdentityCache {
	return &ReadOnlyIdentityCache{inner: inner}
}

func (c *ReadOnlyIdentityCache) GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error) {
	fp, err := c.inner.GetFingerprint(ctx, accountID)
	// A miss is (nil, nil): the command maps the cache client's "not found" to that.
	if err == nil && fp == nil {
		c.missing = true
	} else if err != nil {
		c.readFailed = true
	}
	return fp, err
}

func (c *ReadOnlyIdentityCache) SetFingerprint(context.Context, int64, *Fingerprint) error {
	return nil
}

func (c *ReadOnlyIdentityCache) GetMaskedSessionID(ctx context.Context, accountID int64) (string, error) {
	return c.inner.GetMaskedSessionID(ctx, accountID)
}

func (c *ReadOnlyIdentityCache) SetMaskedSessionID(context.Context, int64, string) error { return nil }

func (c *ReadOnlyIdentityCache) Check() error {
	// Do not expose connection strings or cached identity data in diagnostic errors.
	if c.readFailed {
		return errors.New("identity_cache_read_failed: could not read or decode production fingerprint")
	}
	if c.missing {
		return errors.New("identity_cache_missing: no stored production fingerprint")
	}
	return nil
}

func (k *LongContextCalibrator) service(upstream HTTPUpstream) *GatewayService {
	return &GatewayService{cfg: k.cfg, responseHeaderFilter: compileResponseHeaderFilter(k.cfg), httpUpstream: upstream,
		rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}, tlsFPProfileService: k.tls, settingService: k.settings,
		identityService: k.identity}
}

// calibrationCapture answers locally: count_tokens with 0 (so admission admits) and the
// inference call by recording its body. It never touches the network.
type calibrationCapture struct{ body []byte }

func (u *calibrationCapture) Do(req *http.Request, p string, id int64, c int) (*http.Response, error) {
	return u.DoWithTLS(req, p, id, c, nil)
}

func (u *calibrationCapture) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	if strings.Contains(req.URL.Path, "count_tokens") {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":0}`))}, nil
	}
	u.body = body
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(
		`{"id":"msg_cal","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func calibrationGinContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

// PrepareWireBody runs the real Messages preparation for the account (its settings and
// mimicry included) and returns the exact inference body. No network.
func (k *LongContextCalibrator) PrepareWireBody(ctx context.Context, account *Account, body []byte) ([]byte, error) {
	capture := &calibrationCapture{}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	if err != nil {
		return nil, err
	}
	// The capture never reaches upstream, so admission is not applied to the preparation.
	ctx = context.WithValue(ctx, longContextPreparationOnlyKey{}, true)
	if _, err := k.service(capture).Forward(ctx, calibrationGinContext(), account, parsed); err != nil {
		return nil, err
	}
	if len(capture.body) == 0 {
		return nil, errors.New("preparation produced no inference body")
	}
	return capture.body, nil
}

// buildCountRequest builds the count request exactly as admission does for a non Claude
// Code client on this account, and applies the destination/credential guard.
func (k *LongContextCalibrator) buildCountRequest(ctx context.Context, account *Account, model string, wire []byte) (*http.Request, error) {
	token := account.GetCredential("access_token")
	if token == "" {
		return nil, errors.New("account has no access token")
	}
	req, _, err := k.service(k.upstream).buildCountTokensRequest(WithHTTPUpstreamRedirectsDisabled(ctx), calibrationGinContext(), account, append([]byte(nil), wire...), token, "oauth", model, true)
	if err != nil {
		return nil, err
	}
	return req, checkCalibrationCountRequest(req)
}

// CountOnce sends one count request through the account's proxy and TLS profile.
func (k *LongContextCalibrator) CountOnce(ctx context.Context, account *Account, job calibrationJob) (int64, int, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, calibrationCallTimeout)
	defer cancel()
	req, err := k.buildCountRequest(ctx, account, job.Model, job.Body)
	if err != nil {
		return 0, 0, false, err
	}
	resp, err := k.upstream.DoWithTLS(req, account.Proxy.URL(), account.ID, account.Concurrency, k.tls.ResolveTLSProfile(account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return 0, 0, true, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
		detail := newCalibrationErrorResponse(account, resp, body, readErr)
		log.Printf("calibration_count_error status=%d request_id=%q content_type=%q body=%q truncated=%t read_failed=%t", detail.Status, detail.RequestID, detail.ContentType, detail.Body, detail.Truncated, detail.ReadFailed)
		return 0, resp.StatusCode, readErr != nil, detail
	}
	body, err := readLongContextCountBody(resp.Body)
	if err != nil {
		return 0, resp.StatusCode, true, err
	}
	n, err := parseLongContextCount(body)
	if err != nil {
		return 0, resp.StatusCode, true, err
	}
	return n, resp.StatusCode, false, nil
}

// CheckLongContextCalibrationAccount enforces the approved target before anything is sent.
func CheckLongContextCalibrationAccount(account *Account, wantID int64, now time.Time) error {
	switch {
	case account == nil || account.ID != wantID:
		return errors.New("not the approved account")
	case account.Platform != PlatformAnthropic || account.Type != AccountTypeOAuth:
		return errors.New("approved account is not an Anthropic OAuth account")
	case !account.IsSchedulable():
		return errors.New("account is not schedulable (paused, rate limited, overloaded or temporarily unschedulable)")
	case account.IsCustomBaseURLEnabled():
		return errors.New("account uses a custom base URL")
	case account.Proxy == nil || account.Proxy.URL() == "":
		return errors.New("account has no approved proxy; refusing a direct connection")
	case account.ProxyFallbackOriginID != nil:
		return errors.New("account is currently on a fallback proxy, not its own")
	}
	if exp := account.GetCredentialAsTime("expires_at"); exp != nil && !exp.After(now.Add(time.Minute)) {
		return errors.New("access token expired or about to expire; the harness never refreshes it")
	}
	return nil
}

// LongContextCalibrationReport is written next to the ledger after a live run.
type LongContextCalibrationReport struct {
	Manifest string                               `json:"manifest_sha256"`
	Models   map[string]LongContextCalibrationRow `json:"models"`
}

type LongContextCalibrationRow struct {
	Samples  []calibrationSample `json:"samples"`
	Result   *calibrationResult  `json:"result,omitempty"`
	FitError string              `json:"fit_error,omitempty"`
	// max_tokens -> [largest locally admitted body, smallest locally rejected body]
	Bands map[int64][2]int64 `json:"bands,omitempty"`
}

// prepareCalibrationJobs prepares the original 98-job plan: per model a standalone readiness
// probe, then the 48 frozen fixtures, each through the gateway's own preparation.
func prepareCalibrationJobs(ctx context.Context, k *LongContextCalibrator, account *Account) ([]calibrationJob, error) {
	var jobs []calibrationJob
	for _, model := range calibrationModels {
		ready, err := k.PrepareWireBody(ctx, account, []byte(`{"model":"`+model+`","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`))
		if err != nil {
			return nil, fmt.Errorf("prepare readiness %s: %w", model, err)
		}
		jobs = append(jobs, calibrationJob{Model: model, Fixture: "readiness", Body: ready, Readiness: true})
		for _, f := range calibrationCorpus() {
			wire, err := k.PrepareWireBody(ctx, account, calibrationRequestBody(f, model))
			if err != nil {
				return nil, fmt.Errorf("prepare %s/%s: %w", model, f.Name, err)
			}
			if len(wire) > calibrationMaxBodyBytes {
				return nil, fmt.Errorf("%s/%s prepared body %d bytes exceeds cap", model, f.Name, len(wire))
			}
			jobs = append(jobs, calibrationJob{Model: model, Fixture: f.Name, Set: f.Set, Kind: f.Kind, Body: wire})
		}
	}
	if len(jobs) > calibrationMaxAttempts {
		return nil, fmt.Errorf("%d jobs exceed the %d attempt budget", len(jobs), calibrationMaxAttempts)
	}
	return jobs, nil
}

// calibrationResumeJobs is continuation A: the standalone probes are dropped and each
// model's first fixture doubles as its readiness check. Bodies, sets and kinds are untouched.
func calibrationResumeJobs(original []calibrationJob) []calibrationJob {
	var out []calibrationJob
	seen := map[string]bool{}
	for _, j := range original {
		if j.Set == "" { // standalone readiness probe
			continue
		}
		j.Readiness = !seen[j.Model]
		seen[j.Model] = true
		out = append(out, j)
	}
	return out
}

// preflightCalibrationJobs builds every count request offline: the destination/credential
// guard must pass and the count copy must carry neither metadata nor max_tokens.
func preflightCalibrationJobs(ctx context.Context, k *LongContextCalibrator, account *Account, jobs []calibrationJob, logf func(string, ...any)) error {
	for _, j := range jobs {
		req, err := k.buildCountRequest(ctx, account, j.Model, j.Body)
		if err != nil {
			return fmt.Errorf("preflight %s: %w", j.key(), err)
		}
		rc, err := req.GetBody()
		if err != nil {
			return fmt.Errorf("preflight %s: %w", j.key(), err)
		}
		countBody, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("preflight %s: %w", j.key(), err)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(countBody, &top); err != nil {
			return fmt.Errorf("preflight %s: count body is not a JSON object", j.key())
		}
		for _, field := range []string{"metadata", "max_tokens"} {
			if _, ok := top[field]; ok {
				return fmt.Errorf("preflight %s: count body carries %s", j.key(), field)
			}
		}
		logf("%s %s/%s B=%d sha256=%s readiness=%t", j.Model, j.Set, j.Kind, len(j.Body), j.bodySHA()[:16]+" "+j.Fixture, j.Readiness)
	}
	if k.idCache != nil {
		if err := k.idCache.Check(); err != nil {
			return err
		}
	}
	return nil
}

// RunLongContextCalibration prepares the frozen corpus for the account, verifies every count
// request offline, and (live only) runs the ledgered experiment and writes the report.
func RunLongContextCalibration(ctx context.Context, k *LongContextCalibrator, account *Account, dir string, live bool, logf func(string, ...any)) (*LongContextCalibrationReport, error) {
	jobs, err := prepareCalibrationJobs(ctx, k, account)
	if err != nil {
		return nil, err
	}
	if err := preflightCalibrationJobs(ctx, k, account, jobs, logf); err != nil {
		return nil, err
	}
	manifest := calibrationManifest(account.ID, jobs)
	logf("manifest_sha256=%s jobs=%d", manifest, len(jobs))
	if !live {
		return &LongContextCalibrationReport{Manifest: manifest}, nil
	}
	return runCalibrationJobs(ctx, k, account, dir, jobs, manifest)
}

// CalibrationResumeApproval records who approved continuation A.
const CalibrationResumeApproval = "user: $collab-continue 选 A (2026-11-05); decision .ai-collab/decisions/issue-139-calibration-resume-a.md"

// RunLongContextCalibrationResume is continuation A. The prepared original plan must still
// hash to wantOriginal (the manifest bound by the ledger), and the ledger must hold exactly
// the original stopped history: the Sonnet readiness probe and its authorized diagnostic,
// both HTTP 400. execute=false only verifies and prints; execute=true appends the one-time
// resume entry (once) and runs the remaining 96 jobs under the same cumulative budget.
func RunLongContextCalibrationResume(ctx context.Context, k *LongContextCalibrator, account *Account, dir, wantOriginal, revision string, execute bool, logf func(string, ...any)) (*LongContextCalibrationReport, error) {
	original, err := prepareCalibrationJobs(ctx, k, account)
	if err != nil {
		return nil, err
	}
	oldManifest := calibrationManifest(account.ID, original)
	logf("original_manifest_sha256=%s jobs=%d", oldManifest, len(original))
	if oldManifest != wantOriginal {
		return nil, fmt.Errorf("prepared original plan hashes to %s, not the bound %s; bodies changed", oldManifest, wantOriginal)
	}
	jobs := calibrationResumeJobs(original)
	if err := preflightCalibrationJobs(ctx, k, account, jobs, logf); err != nil {
		return nil, err
	}
	manifest := calibrationManifest(account.ID, jobs)
	logf("resume_manifest_sha256=%s jobs=%d", manifest, len(jobs))
	ledger := filepath.Join(dir, "ledger.jsonl")
	if err := checkCalibrationResume(ledger, oldManifest, manifest); err != nil {
		return nil, err
	}
	if !execute {
		return &LongContextCalibrationReport{Manifest: manifest}, nil
	}
	if revision == "" {
		return nil, errors.New("resume needs the build revision recorded")
	}
	if err := beginCalibrationResume(ledger, oldManifest, manifest, revision, time.Now()); err != nil {
		return nil, err
	}
	return runCalibrationJobs(ctx, k, account, dir, jobs, manifest)
}

// checkCalibrationResume accepts either the exact original stopped history (a resume may
// be appended) or a ledger already resumed to this manifest (continue, no new transition).
func checkCalibrationResume(ledger, oldManifest, newManifest string) error {
	st, err := readCalibrationLedger(ledger)
	if err != nil {
		return fmt.Errorf("%w: %v", errCalibrationStopped, err)
	}
	if len(st.pending) > 0 {
		return fmt.Errorf("%w: %d dispatched attempt(s) have no recorded outcome", errCalibrationStopped, len(st.pending))
	}
	if st.resumed {
		if st.manifest != newManifest {
			return fmt.Errorf("%w: ledger was resumed to a different manifest", errCalibrationStopped)
		}
		if st.stopped != "" {
			return fmt.Errorf("%w: stopped after resume: %s", errCalibrationStopped, st.stopped)
		}
		return nil
	}
	want := map[string]bool{"claude-sonnet-4-6/readiness": true, "claude-sonnet-4-6/readiness-diagnostic-1": true}
	if st.manifest != oldManifest || st.dispatched != len(want) || len(st.results) != len(want) || st.stopped == "" {
		return fmt.Errorf("%w: ledger is not the exact original stopped history", errCalibrationStopped)
	}
	for job, e := range st.results {
		if !want[job] || e.Status != http.StatusBadRequest {
			return fmt.Errorf("%w: unexpected result %s (status %d) in the original history", errCalibrationStopped, job, e.Status)
		}
	}
	return nil
}

// beginCalibrationResume appends the one-time resume entry under the ledger lock, after
// re-validating the history. It is idempotent: an already resumed ledger is left unchanged.
func beginCalibrationResume(ledger, oldManifest, newManifest, revision string, now time.Time) error {
	lock := ledger + ".lock"
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: ledger is locked (%s)", errCalibrationStopped, lock)
	}
	_ = lf.Close()
	defer func() { _ = os.Remove(lock) }()
	if err := checkCalibrationResume(ledger, oldManifest, newManifest); err != nil {
		return err
	}
	st, err := readCalibrationLedger(ledger)
	if err != nil {
		return err
	}
	if st.resumed {
		return nil
	}
	return appendCalibrationLedger(ledger, calibrationLedgerEntry{Phase: "resume", PrevManifest: oldManifest, Manifest: newManifest,
		Reason: "readiness 400 caused by metadata in the count request; fixed", Revision: revision, Approval: CalibrationResumeApproval, At: now})
}

// runCalibrationJobs runs the ledgered experiment and writes report.json. Every fixture with
// a successful result is a sample, including the fixtures that doubled as readiness checks.
func runCalibrationJobs(ctx context.Context, k *LongContextCalibrator, account *Account, dir string, jobs []calibrationJob, manifest string) (*LongContextCalibrationReport, error) {
	ledger := filepath.Join(dir, "ledger.jsonl")
	runner := &calibrationRunner{LedgerPath: ledger, AccountID: account.ID, Now: time.Now, Sleep: time.Sleep,
		Counter: func(j calibrationJob) (int64, int, bool, error) { return k.CountOnce(ctx, account, j) }}
	runErr := runner.run(jobs)
	st, err := readCalibrationLedger(ledger)
	if err != nil {
		return nil, err
	}
	report := buildCalibrationReport(jobs, st, manifest)
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), data, 0o600); err != nil {
		return report, err
	}
	return report, runErr
}

func buildCalibrationReport(jobs []calibrationJob, st *calibrationLedgerState, manifest string) *LongContextCalibrationReport {
	report := &LongContextCalibrationReport{Manifest: manifest, Models: map[string]LongContextCalibrationRow{}}
	for _, model := range calibrationModels {
		row := LongContextCalibrationRow{}
		for _, j := range jobs {
			if r, ok := st.results[j.key()]; ok && j.Model == model && j.Set != "" && r.ErrClass == "" && !st.preResume[j.key()] {
				row.Samples = append(row.Samples, calibrationSample{Fixture: j.Fixture, Set: j.Set, Kind: j.Kind, B: int64(len(j.Body)), C: r.Tokens})
			}
		}
		res, err := fitCalibrationProfile("anthropic-oauth-"+model+"-"+calibrationCorpusVersion, longContextRouteAnthropicOAuth, model, row.Samples, calibrationExpectedCounts)
		if err != nil {
			row.FitError = err.Error()
		} else {
			row.Result = &res
			row.Bands = map[int64][2]int64{}
			for _, o := range []int64{1024, 8192, 32000, 64000} {
				f, r := calibrationBands(res.Profile, o)
				row.Bands[o] = [2]int64{f, r}
			}
		}
		report.Models[model] = row
	}
	return report
}
