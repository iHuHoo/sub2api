import { apiClient } from './client'
import type { SupplierAccount } from './supplier'
import type { CreateAccountRequest, CodexSessionImportRequest, CodexSessionImportResult, OpenAICodexPATCreateRequest } from '@/types'
import type { GeminiAuthUrlRequest, GeminiAuthUrlResponse, GeminiExchangeCodeRequest, GeminiTokenInfo, GeminiOAuthCapabilities } from './admin/gemini'
import type { AntigravityAuthUrlRequest, AntigravityAuthUrlResponse, AntigravityExchangeCodeRequest, AntigravityTokenInfo } from './admin/antigravity'
import type { GrokAuthUrlRequest, GrokAuthUrlResponse, GrokExchangeCodeRequest, GrokTokenInfo, GrokOAuthCapabilities, GrokSSOToOAuthRequest, GrokSSOToOAuthResponse } from './admin/grok'

const authorizationTimeout = { timeout: 120000 }
const accountFields = ['name', 'notes', 'proxy_id', 'expires_at']

function pick(input: object, keys: string[]): Record<string, unknown> {
  const values = input as Record<string, unknown>
  return Object.fromEntries(keys.filter(key => values[key] !== undefined).map(key => [key, values[key]]))
}

function credentialsForSupplier(platform: string, type: string, input: Record<string, unknown>) {
  let keys: string[] = []
  switch (type) {
    case 'apikey':
    case 'upstream':
      keys = ['api_key', 'base_url']
      if (platform === 'gemini') keys.push('tier_id')
      if (['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'command_code', 'cline'].includes(platform)) {
        keys.push('account_mode', 'api_protocol', 'api_base_urls')
      }
      break
    case 'oauth':
      keys = ['access_token', 'refresh_token', 'id_token', 'expires_at', 'token_type', 'scope', 'email']
      if (platform === 'openai') keys.push('chatgpt_account_id', 'chatgpt_user_id', 'organization_id', 'client_id', 'plan_type')
      if (platform === 'anthropic') keys.push('claude_user_id', 'anthropic_user_id')
      if (platform === 'gemini') keys.push('project_id', 'oauth_type', 'tier_id')
      if (platform === 'antigravity') keys.push('project_id', 'plan_type')
      if (platform === 'grok') keys.push('client_id', 'team_id')
      break
    case 'setup-token': keys = ['access_token', 'expires_at']; break
    case 'service_account': keys = ['service_account_json', 'project_id', 'location']; break
    case 'bedrock': keys = ['auth_mode', 'api_key', 'aws_access_key_id', 'aws_secret_access_key', 'aws_session_token', 'aws_region', 'aws_force_global']; break
  }
  const credentials = pick(input, keys)
  if (platform === 'openai' && credentials.base_url === 'https://api.openai.com') credentials.base_url = 'https://api.openai.com/v1'
  return credentials
}

async function post<T>(path: string, payload: object): Promise<T> {
  return (await apiClient.post<T>(path, payload, authorizationTimeout)).data
}

// The shared OAuth composables accept endpoints; never allow arbitrary paths here.
function authEndpoint(endpoint: string): string {
  const paths: Record<string, string> = {
    'accounts/generate-auth-url': 'anthropic/generate-auth-url',
    'accounts/generate-setup-token-url': 'anthropic/generate-setup-token-url',
    'accounts/exchange-code': 'anthropic/exchange-code',
    'accounts/exchange-setup-token-code': 'anthropic/exchange-setup-token-code',
    'accounts/cookie-auth': 'anthropic/cookie-auth',
    'accounts/setup-token-cookie-auth': 'anthropic/setup-token-cookie-auth',
    'openai/generate-auth-url': 'openai/generate-auth-url',
    'openai/exchange-code': 'openai/exchange-code',
    'openai/refresh-token': 'openai/refresh-token'
  }
  const match = /^\/(?:admin|supplier)\/(?:oauth\/)?(.+)$/.exec(endpoint)
  const path = match && Object.prototype.hasOwnProperty.call(paths, match[1]!) ? paths[match[1]!] : undefined
  if (!path) throw new Error('Unsupported supplier authorization operation')
  return `/supplier/oauth/${path}`
}

const accounts = {
  async create(payload: CreateAccountRequest): Promise<SupplierAccount> {
    return (await apiClient.post<SupplierAccount>('/supplier/accounts', {
      ...pick(payload, accountFields), platform: payload.platform, type: payload.type,
      credentials: credentialsForSupplier(payload.platform, payload.type, payload.credentials)
    })).data
  },
  async generateAuthUrl(endpoint: string, payload: Record<string, unknown>): Promise<{ auth_url: string; session_id: string; state?: string }> {
    return post(authEndpoint(endpoint), pick(payload, ['proxy_id']))
  },
  async exchangeCode(endpoint: string, payload: Record<string, unknown>): Promise<Record<string, unknown>> {
    const path = authEndpoint(endpoint)
    if (path.endsWith('/cookie-auth') || path.endsWith('/setup-token-cookie-auth')) {
      return post(path, { ...pick(payload, ['proxy_id']), session_key: payload.session_key ?? payload.code })
    }
    return post(path, pick(payload, ['session_id', 'code', 'state', 'proxy_id']))
  },
  async refreshOpenAIToken(refreshToken: string, proxyId?: number | null, endpoint = '/supplier/oauth/openai/refresh-token', clientId?: string): Promise<Record<string, unknown>> {
    return post(authEndpoint(endpoint), { refresh_token: refreshToken, ...(proxyId ? { proxy_id: proxyId } : {}), ...(clientId ? { client_id: clientId } : {}) })
  },
  async importCodexSession(payload: CodexSessionImportRequest): Promise<CodexSessionImportResult> {
    return post('/supplier/accounts/import-codex-session', pick(payload, [...accountFields, 'content', 'contents']))
  },
  async createOpenAICodexPAT(_payload: OpenAICodexPATCreateRequest): Promise<SupplierAccount> {
    throw new Error('Codex PAT import is not available for suppliers')
  }
}

const grok = {
  async generateAuthUrl(payload: GrokAuthUrlRequest): Promise<GrokAuthUrlResponse> { return post('/supplier/oauth/grok/generate-auth-url', pick(payload, ['proxy_id'])) },
  async exchangeCode(payload: GrokExchangeCodeRequest): Promise<GrokTokenInfo> { return post('/supplier/oauth/grok/exchange-code', pick(payload, ['session_id', 'code', 'state', 'proxy_id'])) },
  async refreshGrokToken(refreshToken: string, proxyId?: number | null): Promise<GrokTokenInfo> { return post('/supplier/oauth/grok/refresh-token', { refresh_token: refreshToken, ...(proxyId ? { proxy_id: proxyId } : {}) }) },
  async validateSSOToken(ssoToken: string, proxyId?: number | null): Promise<GrokTokenInfo> { return post('/supplier/oauth/grok/validate-sso-token', { sso_token: ssoToken, ...(proxyId ? { proxy_id: proxyId } : {}) }) },
  async authorizePassword(emailAndPassword: string, proxyId?: number | null): Promise<GrokTokenInfo> {
    const split = emailAndPassword.indexOf('----')
    return post('/supplier/oauth/grok/authorize-password', { email: (split < 0 ? emailAndPassword : emailAndPassword.slice(0, split)).trim(), password: split < 0 ? '' : emailAndPassword.slice(split + 4), ...(proxyId ? { proxy_id: proxyId } : {}) })
  },
  async getCapabilities(): Promise<GrokOAuthCapabilities> { return (await apiClient.get<GrokOAuthCapabilities>('/supplier/oauth/grok/capabilities')).data },
  async createFromSSO(payload: GrokSSOToOAuthRequest): Promise<GrokSSOToOAuthResponse> {
    const result: GrokSSOToOAuthResponse = { created: [], failed: [] }
    for (const [index, sso] of payload.sso_tokens.entries()) {
      try {
        const token = await grok.validateSSOToken(sso, payload.proxy_id)
        const name = payload.sso_tokens.length > 1 ? `${payload.name || 'Grok'}-${index + 1}` : payload.name || 'Grok'
        const account = await accounts.create({ name, platform: 'grok', type: 'oauth',
          credentials: token, notes: payload.notes, proxy_id: payload.proxy_id, expires_at: payload.expires_at })
        result.created.push({ index, name, account })
      } catch {
        result.failed.push({ index, error: 'SUPPLIER_AUTHORIZATION_FAILED' })
      }
    }
    return result
  }
}

export const supplierAccountCreationAPI = {
  accounts,
  gemini: {
    async generateAuthUrl(payload: GeminiAuthUrlRequest): Promise<GeminiAuthUrlResponse> { return post('/supplier/oauth/gemini/generate-auth-url', pick(payload, ['proxy_id', 'project_id', 'oauth_type', 'tier_id'])) },
    async exchangeCode(payload: GeminiExchangeCodeRequest): Promise<GeminiTokenInfo> { return post('/supplier/oauth/gemini/exchange-code', pick(payload, ['session_id', 'state', 'code', 'proxy_id', 'oauth_type', 'tier_id'])) },
    async getCapabilities(): Promise<GeminiOAuthCapabilities> { return (await apiClient.get<GeminiOAuthCapabilities>('/supplier/oauth/gemini/capabilities')).data }
  },
  antigravity: {
    async generateAuthUrl(payload: AntigravityAuthUrlRequest): Promise<AntigravityAuthUrlResponse> { return post('/supplier/oauth/antigravity/generate-auth-url', pick(payload, ['proxy_id'])) },
    async exchangeCode(payload: AntigravityExchangeCodeRequest): Promise<AntigravityTokenInfo> { return post('/supplier/oauth/antigravity/exchange-code', pick(payload, ['session_id', 'state', 'code', 'proxy_id'])) },
    async refreshAntigravityToken(refreshToken: string, proxyId?: number | null): Promise<AntigravityTokenInfo> { return post('/supplier/oauth/antigravity/refresh-token', { refresh_token: refreshToken, ...(proxyId ? { proxy_id: proxyId } : {}) }) }
  },
  grok
}
