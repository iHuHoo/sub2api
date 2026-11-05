//go:build unit || calibration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type calibrationErrorResponse struct {
	Status      int    `json:"status"`
	RequestID   string `json:"request_id,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
	Truncated   bool   `json:"truncated,omitempty"`
	ReadFailed  bool   `json:"read_failed,omitempty"`
}

func (e *calibrationErrorResponse) Error() string {
	return fmt.Sprintf("count status %d (details saved in ledger and log)", e.Status)
}

func newCalibrationErrorResponse(account *Account, resp *http.Response, body []byte, readErr error) *calibrationErrorResponse {
	redact := func(s string) string {
		secrets := []string{}
		for _, value := range account.Credentials {
			if v, ok := value.(string); ok && v != "" {
				secrets = append(secrets, v)
			}
		}
		if account.Proxy != nil {
			secrets = append(secrets, account.Proxy.URL(), account.Proxy.Username, account.Proxy.Password)
		}
		// A capped or failed read may end halfway through a secret.
		if s == string(body) && (len(body) > 64<<10 || readErr != nil) {
			for _, secret := range secrets {
				encoded, _ := json.Marshal(secret)
				for _, candidate := range []string{secret, string(encoded[1 : len(encoded)-1])} {
					for n := min(len(s), len(candidate)-1); n > 0; n-- {
						if strings.HasSuffix(s, candidate[:n]) {
							s = s[:len(s)-n] + "[REDACTED_PARTIAL]"
							break
						}
					}
				}
			}
		}
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
			encoded, _ := json.Marshal(secret)
			s = strings.ReplaceAll(s, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
		return s
	}
	// Redact before truncation so a credential crossing the output boundary cannot leak.
	raw := redact(string(body))
	clean, truncated := sanitizeErrorBodyForStorage(raw, 64<<10)
	return &calibrationErrorResponse{Status: resp.StatusCode, RequestID: redact(resp.Header.Get("request-id")), ContentType: redact(resp.Header.Get("Content-Type")), Body: clean, Truncated: truncated || len(body) > 64<<10, ReadFailed: readErr != nil}
}

// RunLongContextReadinessDiagnostic is the single additional attempt authorized after
// the first readiness 400. It appends to the same budget ledger and never clears its stop.
func RunLongContextReadinessDiagnostic(ctx context.Context, k *LongContextCalibrator, account *Account, dir string) error {
	if err := CheckLongContextCalibrationAccount(account, 71, time.Now()); err != nil {
		return err
	}
	ledger := filepath.Join(dir, "ledger.jsonl")
	lock := ledger + ".lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("diagnostic refused: ledger locked")
	}
	_ = f.Close()
	defer func() { _ = os.Remove(lock) }()
	st, err := readCalibrationLedger(ledger)
	if err != nil {
		return err
	}
	previous, ok := st.results["claude-sonnet-4-6/readiness"]
	if !ok || previous.Status != 400 || st.stopped == "" || st.dispatched != 1 || len(st.results) != 1 || len(st.pending) != 0 {
		return errors.New("diagnostic refused: requires exactly the original stopped 400; additional attempt already consumed or ledger differs")
	}
	wire, err := k.PrepareWireBody(ctx, account, []byte(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		return err
	}
	job := calibrationJob{Model: "claude-sonnet-4-6", Fixture: "readiness-diagnostic-1", Body: wire, Readiness: true}
	if job.bodySHA() != previous.BodySHA {
		return errors.New("diagnostic refused: readiness body changed")
	}
	if _, err = k.buildCountRequest(ctx, account, job.Model, job.Body); err != nil {
		return err
	}
	if k.idCache != nil {
		if err = k.idCache.Check(); err != nil {
			return err
		}
	}
	if wait := calibrationMinInterval - time.Since(st.last); wait > 0 {
		time.Sleep(wait)
	}
	entry := calibrationLedgerEntry{Seq: 2, Phase: "dispatch", Job: job.key(), BodySHA: job.bodySHA(), BodyB: int64(len(wire)), At: time.Now()}
	if err = appendCalibrationLedger(ledger, entry); err != nil {
		return err
	}
	tokens, status, transport, callErr := k.CountOnce(ctx, account, job)
	entry.Phase = "result"
	entry.At = time.Now()
	entry.Status = status
	entry.Tokens = tokens
	entry.Terminal = "authorized diagnostic completed; calibration remains stopped"
	if callErr != nil {
		entry.ErrClass = fmt.Sprintf("status_%d", status)
		if transport {
			entry.ErrClass = "transport"
		}
	}
	var detail *calibrationErrorResponse
	if errors.As(callErr, &detail) {
		entry.Response = detail
	}
	if err = appendCalibrationLedger(ledger, entry); err != nil {
		return err
	}
	return callErr
}
