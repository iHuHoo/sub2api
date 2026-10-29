package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	supplierAccountTestContextKey        = "supplier_account_test"
	supplierAccountTestStartedContextKey = "supplier_account_test_started"
)

type supplierAccountTestKey struct{}

func isSupplierAccountTestContext(ctx context.Context) bool {
	value, _ := ctx.Value(supplierAccountTestKey{}).(bool)
	return value
}

type supplierReadOnlyAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *supplierReadOnlyAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	clone := *r.account
	clone.Credentials, _ = cloneAccountJSONMap(r.account.Credentials)
	clone.Extra, _ = cloneAccountJSONMap(r.account.Extra)
	return &clone, nil
}

func (*supplierReadOnlyAccountRepo) Update(context.Context, *Account) error        { return nil }
func (*supplierReadOnlyAccountRepo) SetError(context.Context, int64, string) error { return nil }
func (*supplierReadOnlyAccountRepo) ClearError(context.Context, int64) error       { return nil }
func (*supplierReadOnlyAccountRepo) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}
func (*supplierReadOnlyAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}
func (*supplierReadOnlyAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (*supplierReadOnlyAccountRepo) ClearTempUnschedulable(context.Context, int64) error { return nil }
func (*supplierReadOnlyAccountRepo) ClearRateLimit(context.Context, int64) error         { return nil }
func (*supplierReadOnlyAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func (s *AccountTestService) SupplierAvailableModels(ctx context.Context, owner, accountID int64) ([]AccountTestModel, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrSupplierUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil || account.SupplierUserID == nil || *account.SupplierUserID != owner {
		return nil, ErrSupplierUnavailable
	}
	return accountTestModelView(AvailableAccountTestModels(ctx, s, account)), nil
}

func (s *AccountTestService) TestSupplierAccountConnection(c *gin.Context, owner, accountID int64, modelID string) error {
	if s == nil || s.accountRepo == nil {
		return ErrSupplierUnavailable
	}
	account, err := s.accountRepo.GetByID(c.Request.Context(), accountID)
	if err != nil || account == nil || account.SupplierUserID == nil || *account.SupplierUserID != owner {
		return ErrSupplierUnavailable
	}

	repo := &supplierReadOnlyAccountRepo{AccountRepository: s.accountRepo, account: account}
	var gemini *GeminiTokenProvider
	if s.geminiTokenProvider != nil {
		gemini = NewGeminiTokenProvider(repo, nil, nil)
	}
	var claude *ClaudeTokenProvider
	if s.claudeTokenProvider != nil {
		claude = NewClaudeTokenProvider(repo, nil, nil)
	}
	var grok *GrokTokenProvider
	if s.grokTokenProvider != nil {
		grok = NewGrokTokenProvider(repo, nil)
	}
	var antigravity *AntigravityGatewayService
	if s.antigravityGatewayService != nil {
		provider := NewAntigravityTokenProvider(repo, nil, nil)
		antigravity = NewAntigravityGatewayService(repo, nil, nil, provider, nil, s.antigravityGatewayService.httpUpstream, s.antigravityGatewayService.settingService, nil)
	}
	readOnly := NewAccountTestService(repo, gemini, claude, grok, antigravity, s.httpUpstream, s.cfg, s.tlsFPProfileService)
	readOnly.settingService = s.settingService
	readOnly.pluginManager = s.pluginManager
	readOnly.openaiGatewayService = s.openaiGatewayService
	readOnly.agentIdentityWS = s.agentIdentityWS
	readOnly.grokWSDialer = s.grokWSDialer

	c.Set(supplierAccountTestContextKey, true)
	c.Set(supplierAccountTestStartedContextKey, time.Now())
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), supplierAccountTestKey{}, true))
	return readOnly.TestAccountConnection(c, accountID, modelID, "hi", AccountTestModeDefault)
}
