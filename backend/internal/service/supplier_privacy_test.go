//go:build unit

package service

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type supplierPrivacyRepo struct {
	SupplierRepository
	account   *Account
	committed atomic.Bool
	writeErr  error
	entered   chan struct{}
	release   chan struct{}
}

func (r *supplierPrivacyRepo) GetAccount(context.Context, int64, int64) (*Account, error) {
	return r.account, nil
}
func (r *supplierPrivacyRepo) CreateAccount(_ context.Context, _ int64, a *Account) error {
	if r.entered != nil {
		close(r.entered)
		<-r.release
	}
	if r.writeErr != nil {
		return r.writeErr
	}
	a.ID = 95
	r.account = a
	r.committed.Store(true)
	return nil
}
func (r *supplierPrivacyRepo) UpdateAccount(_ context.Context, _, _ int64, in SupplierAccountUpdate) (*Account, error) {
	if r.writeErr != nil {
		return nil, r.writeErr
	}
	a := *r.account
	a.Credentials = maps.Clone(a.Credentials)
	for k, v := range in.Credentials {
		a.Credentials[k] = v
	}
	r.account = &a
	r.committed.Store(true)
	return &a, nil
}

type supplierPrivacyCall struct {
	method    string
	id        int64
	committed bool
	detached  bool
}
type supplierPrivacyFake struct {
	repo        *supplierPrivacyRepo
	calls       chan supplierPrivacyCall
	panicValue  string
	mode        string
	mutateExtra bool
	done        chan struct{}
}

func (f *supplierPrivacyFake) call(ctx context.Context, a *Account, method string) string {
	if f.done != nil {
		defer close(f.done)
	}
	f.calls <- supplierPrivacyCall{method, a.ID, f.repo.committed.Load(), ctx.Err() == nil && ctx.Done() == nil}
	if f.panicValue != "" {
		panic(f.panicValue)
	}
	if f.mutateExtra {
		a.Extra["privacy_mode"] = "privacy_set"
		a.Credentials["access_token"] = "private-snapshot"
	}
	return f.mode
}
func (f *supplierPrivacyFake) EnsureOpenAIPrivacy(c context.Context, a *Account) string {
	return f.call(c, a, "ensure_openai")
}
func (f *supplierPrivacyFake) EnsureAntigravityPrivacy(c context.Context, a *Account) string {
	return f.call(c, a, "ensure_antigravity")
}
func (f *supplierPrivacyFake) ForceOpenAIPrivacy(c context.Context, a *Account) string {
	return f.call(c, a, "force_openai")
}
func (f *supplierPrivacyFake) ForceAntigravityPrivacy(c context.Context, a *Account) string {
	return f.call(c, a, "force_antigravity")
}
func newSupplierPrivacyFake(r *supplierPrivacyRepo) *supplierPrivacyFake {
	return &supplierPrivacyFake{repo: r, calls: make(chan supplierPrivacyCall, 8)}
}
func privacyCall(t *testing.T, f *supplierPrivacyFake, method string) {
	t.Helper()
	select {
	case got := <-f.calls:
		require.Equal(t, method, got.method)
		require.EqualValues(t, 95, got.id)
		require.True(t, got.committed, "privacy must run after repository commit")
		require.True(t, got.detached, "privacy must not inherit request cancellation")
	case <-time.After(time.Second):
		t.Fatal("missing privacy call")
	}
	noPrivacyCall(t, f)
}
func noPrivacyCall(t *testing.T, f *supplierPrivacyFake) {
	t.Helper()
	select {
	case <-f.calls:
		t.Fatal("unexpected or duplicate privacy call")
	case <-time.After(50 * time.Millisecond):
	}
}
func TestSupplierPrivacyCreate(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			repo := &supplierPrivacyRepo{entered: make(chan struct{}), release: make(chan struct{})}
			f := newSupplierPrivacyFake(repo)
			s := NewSupplierService(repo, nil, f)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			done := make(chan error, 1)
			go func() {
				_, err := s.CreateAccount(ctx, 11, SupplierAccountCreate{Name: "oauth", Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fake-token"}})
				done <- err
			}()
			select {
			case <-repo.entered:
			case <-time.After(time.Second):
				t.Fatal("repository not entered")
			}
			// Holding the repository before commit makes the before-write mutation deterministic.
			select {
			case <-f.calls:
				close(repo.release)
				t.Fatal("privacy ran before commit")
			case <-time.After(50 * time.Millisecond):
			}
			close(repo.release)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("create blocked on privacy")
			}
			privacyCall(t, f, "ensure_"+platform)
		})
	}
}
func TestSupplierPrivacyCreateExcluded(t *testing.T) {
	cases := []struct {
		platform, typ string
		credentials   map[string]any
	}{
		{PlatformOpenAI, AccountTypeAPIKey, map[string]any{"api_key": "fake-key"}},
		{PlatformAnthropic, AccountTypeOAuth, map[string]any{"access_token": "fake-token"}},
		{PlatformGemini, AccountTypeOAuth, map[string]any{"access_token": "fake-token"}},
		{PlatformGrok, AccountTypeOAuth, map[string]any{"access_token": "fake-token"}},
	}
	for _, tc := range cases {
		t.Run(tc.platform+"/"+tc.typ, func(t *testing.T) {
			r := &supplierPrivacyRepo{}
			f := newSupplierPrivacyFake(r)
			_, err := NewSupplierService(r, nil, f).CreateAccount(context.Background(), 11, SupplierAccountCreate{Name: "other", Platform: tc.platform, Type: tc.typ, Credentials: tc.credentials})
			require.NoError(t, err)
			noPrivacyCall(t, f)
		})
	}
}
func TestSupplierPrivacyWriteFailure(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
			r := &supplierPrivacyRepo{account: &Account{ID: 95, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old"}}, writeErr: errors.New("write failed")}
			f := newSupplierPrivacyFake(r)
			s := NewSupplierService(r, nil, f)
			var err error
			if update {
				_, err = s.UpdateAccount(context.Background(), 11, 95, SupplierAccountUpdate{Credentials: map[string]any{"access_token": "new"}})
			} else {
				_, err = s.CreateAccount(context.Background(), 11, SupplierAccountCreate{Name: "oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "new"}})
			}
			require.ErrorIs(t, err, r.writeErr)
			noPrivacyCall(t, f)
		})
	}
}
func TestSupplierPrivacyUpdate(t *testing.T) {
	name, notes := "renamed", "note"
	for _, platform := range []string{PlatformOpenAI, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				in     SupplierAccountUpdate
				method string
			}{
				{"rotated", SupplierAccountUpdate{Credentials: map[string]any{"access_token": "new"}}, "force_" + platform},
				{"unchanged", SupplierAccountUpdate{Credentials: map[string]any{"access_token": "old"}}, "ensure_" + platform},
				{"refresh_only", SupplierAccountUpdate{Credentials: map[string]any{"refresh_token": "new-refresh"}}, "ensure_" + platform},
				{"name", SupplierAccountUpdate{Name: &name}, ""}, {"notes", SupplierAccountUpdate{Notes: &notes}, ""},
				{"proxy", SupplierAccountUpdate{ProxyID: ptrInt64(12)}, ""}, {"expiry", SupplierAccountUpdate{ExpiresAt: ptrInt64(time.Now().Add(time.Hour).Unix())}, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := &supplierPrivacyRepo{account: &Account{ID: 95, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old"}, Extra: map[string]any{"privacy_mode": "privacy_set"}}}
					f := newSupplierPrivacyFake(r)
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					_, err := NewSupplierService(r, nil, f).UpdateAccount(ctx, 11, 95, tc.in)
					require.NoError(t, err)
					if tc.method == "" {
						noPrivacyCall(t, f)
					} else {
						privacyCall(t, f, tc.method)
					}
				})
			}
		})
	}
}
func TestSupplierPrivacyDerivedUpdate(t *testing.T) {
	r := &supplierPrivacyRepo{account: &Account{ID: 95, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: ptrInt64(90)}}
	f := newSupplierPrivacyFake(r)
	_, err := NewSupplierService(r, nil, f).UpdateAccount(context.Background(), 11, 95, SupplierAccountUpdate{Credentials: map[string]any{"access_token": "new"}})
	require.ErrorIs(t, err, ErrSupplierReadOnly)
	require.False(t, r.committed.Load())
	noPrivacyCall(t, f)
}

// Log records provide a completion signal after panic recovery, without polling.
type supplierPrivacyLogHandler struct{ records chan slog.Record }

func (h supplierPrivacyLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h supplierPrivacyLogHandler) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.records <- r.Clone():
	default:
	}
	return nil
}
func (h supplierPrivacyLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h supplierPrivacyLogHandler) WithGroup(string) slog.Handler      { return h }
func TestSupplierPrivacyPanicDoesNotLeakCredentials(t *testing.T) {
	const marker = "synthetic-privacy-token-must-not-be-logged"
	logs := make(chan slog.Record, 8)
	previous := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	// Cleanup runs in reverse order: verify the restoration below after it runs.
	t.Cleanup(func() {
		if log.Writer() != previousWriter || log.Flags() != previousFlags {
			t.Error("standard logger output and flags were not restored")
		}
	})
	slog.SetDefault(slog.New(supplierPrivacyLogHandler{logs}))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	r := &supplierPrivacyRepo{}
	f := newSupplierPrivacyFake(r)
	f.panicValue = marker
	_, err := NewSupplierService(r, nil, f).CreateAccount(context.Background(), 11, SupplierAccountCreate{Name: "oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": marker}})
	require.NoError(t, err)
	privacyCall(t, f, "ensure_openai")
	select {
	case record := <-logs:
		var text strings.Builder
		text.WriteString(record.Message)
		keys := []string{}
		record.Attrs(func(a slog.Attr) bool { keys = append(keys, a.Key); text.WriteString(a.String()); return true })
		require.NotContains(t, text.String(), marker)
		require.ElementsMatch(t, []string{"account_id", "platform"}, keys)
	case <-time.After(time.Second):
		t.Fatal("missing recovery log")
	}
}
func TestSupplierPrivacyFailureDoesNotFailCreate(t *testing.T) {
	r := &supplierPrivacyRepo{}
	f := newSupplierPrivacyFake(r)
	f.mode = PrivacyModeFailed
	_, err := NewSupplierService(r, nil, f).CreateAccount(context.Background(), 11, SupplierAccountCreate{Name: "oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fake-token"}})
	require.NoError(t, err)
	privacyCall(t, f, "ensure_openai")
}

func TestSupplierPrivacySnapshotIsolatesResponse(t *testing.T) {
	r := &supplierPrivacyRepo{account: &Account{ID: 95, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old"}, Extra: map[string]any{"privacy_mode": "original"}}}
	f := newSupplierPrivacyFake(r)
	f.mutateExtra = true
	f.done = make(chan struct{})
	_, err := NewSupplierService(r, nil, f).UpdateAccount(context.Background(), 11, 95, SupplierAccountUpdate{Credentials: map[string]any{"access_token": "new"}})
	require.NoError(t, err)
	select {
	case <-f.done:
	case <-time.After(time.Second):
		t.Fatal("privacy did not finish")
	}
	privacyCall(t, f, "force_antigravity")
	require.Equal(t, "original", r.account.Extra["privacy_mode"])
	require.Equal(t, "new", r.account.Credentials["access_token"])
}

type supplierShadowPrivacy struct {
	AdminService
	done chan string
}

func (s *supplierShadowPrivacy) EnsureOpenAIPrivacy(ctx context.Context, a *Account) string {
	mode := s.AdminService.EnsureOpenAIPrivacy(ctx, a)
	s.done <- mode
	return mode
}
func (s *supplierShadowPrivacy) ForceOpenAIPrivacy(ctx context.Context, a *Account) string {
	mode := s.AdminService.ForceOpenAIPrivacy(ctx, a)
	s.done <- mode
	return mode
}
func TestSupplierPrivacyDelegatesShadowGuard(t *testing.T) {
	for _, force := range []bool{false, true} {
		var calls atomic.Int32
		privacy := &supplierShadowPrivacy{AdminService: &adminServiceImpl{privacyClientFactory: func(string) (*req.Client, error) { calls.Add(1); return nil, errors.New("must not call") }}, done: make(chan string, 1)}
		svc := NewSupplierService(nil, nil, privacy)
		svc.ensurePrivacyAsync(&Account{ID: 95, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: ptrInt64(90), Credentials: map[string]any{"access_token": "fake-token"}}, force)
		select {
		case mode := <-privacy.done:
			require.Empty(t, mode)
		case <-time.After(time.Second):
			t.Fatal("existing shadow guard was not delegated to")
		}
		require.Zero(t, calls.Load())
	}
}
