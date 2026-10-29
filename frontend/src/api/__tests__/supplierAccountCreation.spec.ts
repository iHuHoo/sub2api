import { beforeEach, describe, expect, it, vi } from 'vitest'
import { supplierAccountCreationAPI } from '../supplierAccountCreation'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

describe('supplier shared account creation transport', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    client.post.mockResolvedValue({ data: { id: 42 } })
    client.get.mockResolvedValue({ data: {} })
  })
  it('sends only supplier account and credential fields from the shared administrator form', async () => {
    await supplierAccountCreationAPI.accounts.create({
      name: 'Owned', platform: 'openai', type: 'oauth', notes: 'note', proxy_id: 7,
      concurrency: 10, priority: 1, group_ids: [2], rate_multiplier: 1,
      extra: { openai_passthrough: true },
      credentials: { access_token: 'synthetic', refresh_token: 'refresh', chatgpt_account_id: 'account',
        model_mapping: { '*': 'bad' }, base_url: 'https://untrusted.example', header_overrides: { Authorization: 'bad' } }
    })
    expect(client.post).toHaveBeenCalledWith('/supplier/accounts', {
      name: 'Owned', platform: 'openai', type: 'oauth', notes: 'note', proxy_id: 7,
      credentials: { access_token: 'synthetic', refresh_token: 'refresh', chatgpt_account_id: 'account' }
    })
  })
  it.each([
    ['/admin/accounts/generate-auth-url', '/supplier/oauth/anthropic/generate-auth-url'],
    ['/supplier/accounts/generate-setup-token-url', '/supplier/oauth/anthropic/generate-setup-token-url'],
    ['/admin/openai/generate-auth-url', '/supplier/oauth/openai/generate-auth-url']
  ])('maps only declared OAuth endpoints: %s', async (source, target) => {
    await supplierAccountCreationAPI.accounts.generateAuthUrl(source, { proxy_id: 7 })
    expect(client.post).toHaveBeenCalledWith(target, { proxy_id: 7 }, { timeout: 120000 })
  })
  it('rejects arbitrary endpoints before making a request', async () => {
    await expect(supplierAccountCreationAPI.accounts.generateAuthUrl('/admin/settings', {})).rejects.toThrow()
    expect(client.post).not.toHaveBeenCalled()
  })
  it('preserves OAuth session and state but drops redirect and management data', async () => {
    await supplierAccountCreationAPI.accounts.exchangeCode('/supplier/oauth/openai/exchange-code', {
      session_id: 'owned-session', code: 'code', state: 'state', proxy_id: 7,
      redirect_uri: 'https://untrusted.example', group_ids: [1]
    })
    expect(client.post).toHaveBeenCalledWith('/supplier/oauth/openai/exchange-code', {
      session_id: 'owned-session', code: 'code', state: 'state', proxy_id: 7
    }, { timeout: 120000 })
  })
  it('imports sessions without update-existing or administrator overrides', async () => {
    await supplierAccountCreationAPI.accounts.importCodexSession({ content: '{}', name: 'Owned', proxy_id: 7,
      update_existing: true, group_ids: [2], credential_extras: { base_url: 'https://evil.example' }, extra: { x: true } })
    expect(client.post).toHaveBeenCalledWith('/supplier/accounts/import-codex-session', {
      content: '{}', name: 'Owned', proxy_id: 7
    }, { timeout: 120000 })
  })
  it('routes platform authorizations and capability queries only to supplier endpoints', async () => {
    await supplierAccountCreationAPI.gemini.generateAuthUrl({ oauth_type: 'code_assist', project_id: 'project', proxy_id: 7 })
    await supplierAccountCreationAPI.antigravity.refreshAntigravityToken('refresh', 7)
    await supplierAccountCreationAPI.grok.validateSSOToken('synthetic', 7)
    await supplierAccountCreationAPI.gemini.getCapabilities()
    await supplierAccountCreationAPI.grok.getCapabilities()
    expect(client.post.mock.calls.map(call => call[0])).toEqual([
      '/supplier/oauth/gemini/generate-auth-url', '/supplier/oauth/antigravity/refresh-token', '/supplier/oauth/grok/validate-sso-token'
    ])
    expect(client.get.mock.calls.map(call => call[0])).toEqual(['/supplier/oauth/gemini/capabilities', '/supplier/oauth/grok/capabilities'])
  })
  it('adapts the shared cookie form to the supplier cookie contract', async () => {
    await supplierAccountCreationAPI.accounts.exchangeCode('/admin/accounts/cookie-auth', { session_id: '', code: 'synthetic-cookie', proxy_id: 7 })
    expect(client.post).toHaveBeenCalledWith('/supplier/oauth/anthropic/cookie-auth', { session_key: 'synthetic-cookie', proxy_id: 7 }, { timeout: 120000 })
  })
  it.each([
    ['openai', 'oauth', { access_token: 'a', client_id: 'refresh-client', plan_type: 'pro' }, { access_token: 'a', client_id: 'refresh-client', plan_type: 'pro' }],
    ['gemini', 'apikey', { api_key: 'a', tier_id: 'aistudio_paid' }, { api_key: 'a', tier_id: 'aistudio_paid' }],
    ['gemini', 'oauth', { access_token: 'a', oauth_type: 'google_one', tier_id: 'google_one_free', project_id: 'project' }, { access_token: 'a', oauth_type: 'google_one', tier_id: 'google_one_free', project_id: 'project' }],
    ['gemini', 'service_account', { auth_mode: 'apikey', api_key: ' vertex ', location: 'global', tier_id: 'vertex' }, { auth_mode: 'apikey', api_key: ' vertex ', tier_id: 'vertex' }],
    ['gemini', 'service_account', { auth_mode: 'apikey', api_key: 'vertex', project_id: 'project', tier_id: 'vertex' }, { auth_mode: 'apikey', api_key: 'vertex', project_id: 'project', location: 'global', tier_id: 'vertex' }],
    ['anthropic', 'service_account', { service_account_json: '{}', project_id: 'project', location: 'global', auth_mode: 'apikey', api_key: 'forbidden', tier_id: 'vertex' }, { service_account_json: '{}', project_id: 'project', location: 'global' }],
    ['openai', 'apikey', { api_key: 'a', base_url: 'https://api.openai.com' }, { api_key: 'a', base_url: 'https://api.openai.com/v1' }],
    ['anthropic', 'bedrock', { auth_mode: 'apikey', api_key: 'a', aws_region: 'us-east-1', aws_force_global: 'true' }, { auth_mode: 'apikey', api_key: 'a', aws_region: 'us-east-1', aws_force_global: 'true' }],
    ['kimi', 'apikey', { api_key: 'a', account_mode: 'coding', api_protocol: 'adaptive', api_base_urls: { anthropic: 'https://api.kimi.com/coding' }, model_mapping: { '*': 'x' } }, { api_key: 'a', account_mode: 'coding', api_protocol: 'adaptive', api_base_urls: { anthropic: 'https://api.kimi.com/coding' } }]
  ])('preserves required credential semantics for %s / %s', async (platform, type, credentials, expected) => {
    await supplierAccountCreationAPI.accounts.create({ name: 'Owned', platform, type, credentials } as Parameters<typeof supplierAccountCreationAPI.accounts.create>[0])
    expect(client.post.mock.calls[0]?.[1].credentials).toEqual(expected)
  })
  it('creates Grok SSO imports through owned-account creation and sanitizes individual failures', async () => {
    client.post.mockImplementation(async (path: string, payload: Record<string, unknown>) => {
      if (path.endsWith('/validate-sso-token')) {
        if (payload.sso_token === 'bad') throw new Error('raw-cookie-and-proxy-password')
        return { data: { access_token: 'access', client_id: 'refresh-client', team_id: 'team' } }
      }
      return { data: { id: 42 } }
    })
    const result = await supplierAccountCreationAPI.grok.createFromSSO({ name: 'Grok', sso_tokens: ['good', 'bad'], group_ids: [1], extra: { secret: 'no' } })
    expect(result.created).toHaveLength(1)
    expect(result.failed).toEqual([{ index: 1, error: 'SUPPLIER_AUTHORIZATION_FAILED' }])
    expect(client.post.mock.calls.find(call => call[0] === '/supplier/accounts')?.[1]).toEqual({ name: 'Grok-1', platform: 'grok', type: 'oauth', credentials: { access_token: 'access', client_id: 'refresh-client', team_id: 'team' } })
  })
})
