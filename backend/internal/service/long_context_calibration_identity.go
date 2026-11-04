//go:build unit || calibration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/redis/go-redis/v9"
)

// The snapshot contains identity only, never account credentials or proxy credentials.
type calibrationIdentitySnapshot struct {
	AccountID   int64        `json:"account_id"`
	Fingerprint *Fingerprint `json:"fingerprint"`
	Masking     bool         `json:"masking"`
	Session     string       `json:"session,omitempty"`
}

func (s *calibrationIdentitySnapshot) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	if s.Fingerprint == nil {
		return nil, nil
	}
	fp := *s.Fingerprint
	return &fp, nil
}
func (s *calibrationIdentitySnapshot) SetFingerprint(_ context.Context, _ int64, fp *Fingerprint) error {
	copy := *fp
	s.Fingerprint = &copy
	return nil
}
func (s *calibrationIdentitySnapshot) GetMaskedSessionID(context.Context, int64) (string, error) {
	return s.Session, nil
}
func (s *calibrationIdentitySnapshot) SetMaskedSessionID(_ context.Context, _ int64, id string) error {
	s.Session = id
	return nil
}

// NewCalibrationIdentitySnapshot freezes the production identity or its normal missing-cache
// fallback locally. Later invocations must reuse it, even if production Redis has changed.
func NewCalibrationIdentitySnapshot(ctx context.Context, inner IdentityCache, account *Account, dir string) (*ReadOnlyIdentityCache, error) {
	path := filepath.Join(dir, "identity.json")
	snapshot := &calibrationIdentitySnapshot{}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, snapshot) != nil || snapshot.AccountID != account.ID || snapshot.Fingerprint == nil || snapshot.Fingerprint.ClientID == "" || snapshot.Fingerprint.UserAgent == "" || snapshot.Masking != account.IsSessionIDMaskingEnabled() || (snapshot.Masking && snapshot.Session == "") {
			return nil, errors.New("identity_snapshot_invalid: refusing to replace stored identity")
		}
		return NewReadOnlyIdentityCache(snapshot), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("identity_snapshot_read_failed")
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger.jsonl")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("identity_snapshot_missing_with_ledger: refusing to generate a replacement")
	}
	fp, err := inner.GetFingerprint(ctx, account.ID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, errors.New("identity_cache_read_failed: could not read or decode production fingerprint")
	}
	if errors.Is(err, redis.Nil) {
		fp = nil
	}
	snapshot.AccountID = account.ID
	snapshot.Fingerprint = fp
	snapshot.Masking = account.IsSessionIDMaskingEnabled()
	// Reuse the gateway's generation and normalization rules; its writes stay in memory.
	fp, err = NewIdentityService(snapshot).GetOrCreateFingerprint(ctx, account.ID, http.Header{})
	if err != nil {
		return nil, errors.New("identity_snapshot_generation_failed")
	}
	snapshot.Fingerprint = fp
	if snapshot.Masking {
		snapshot.Session, err = inner.GetMaskedSessionID(ctx, account.ID)
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, errors.New("identity_cache_read_failed: could not read masked session")
		}
		if errors.Is(err, redis.Nil) || snapshot.Session == "" {
			snapshot.Session = generateRandomUUID()
		}
	}
	data, err = json.Marshal(snapshot)
	if err != nil {
		return nil, errors.New("identity_snapshot_encode_failed")
	}
	// Exclusive creation prevents concurrent runs from replacing each other's identity.
	// A partial write is retained and rejected on restart, never silently regenerated.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("identity_snapshot_create_failed")
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.New("identity_snapshot_write_failed")
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, errors.New("identity_snapshot_directory_sync_failed")
	}
	syncErr := d.Sync()
	closeErr = d.Close()
	if syncErr != nil || closeErr != nil {
		return nil, errors.New("identity_snapshot_directory_sync_failed")
	}
	return NewReadOnlyIdentityCache(snapshot), nil
}
