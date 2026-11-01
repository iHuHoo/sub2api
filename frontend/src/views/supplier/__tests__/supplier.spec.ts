import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import AccountsView from '../AccountsView.vue'
import ProxiesView from '../ProxiesView.vue'
import UsageView from '../UsageView.vue'
import type { SupplierUsage } from '@/api/supplier'
import { formatDecimalCurrency } from '@/utils/format'

const api = vi.hoisted(() => ({
  accounts: { list: vi.fn(), create: vi.fn(), update: vi.fn(), getUsage: vi.fn(), getModels: vi.fn() },
  proxies: { list: vi.fn(), create: vi.fn(), update: vi.fn(), test: vi.fn(), delete: vi.fn() },
  usage: vi.fn(),
  showError: vi.fn(), showSuccess: vi.fn()
}))
const admin = vi.hoisted(() => ({
  getUsage: vi.fn(),
  getClaudeResetCredits: vi.fn(),
  queryQuota: vi.fn(),
  queryBalance: vi.fn(),
  getAvailableModels: vi.fn()
}))
vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options'], template: '<canvas />' }
}))
vi.mock('@/api/supplier', () => ({ supplierAPI: api, supplierErrorKey: () => 'supplier.failed' }))
vi.mock('@/stores/app', () => ({ useAppStore: () => api }))
vi.mock('@/utils/format', async (original) => ({
  ...await original<typeof import('@/utils/format')>(),
  formatRelativeTime: () => '5m ago'
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getUsage: admin.getUsage, getAvailableModels: admin.getAvailableModels },
    cnProviders: { queryQuota: admin.queryQuota, queryBalance: admin.queryBalance }
  }
}))
vi.mock('@/api/admin/claudeResetCredits', () => ({
  getClaudeResetCredits: admin.getClaudeResetCredits,
  redeemClaudeResetCredit: vi.fn()
}))
vi.mock('vue-i18n', async (original) => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, params: Record<string, unknown> = {}) => {
    const value = key.split('.').reduce((value: unknown, part) => (value as Record<string, unknown>)?.[part], en)
    return String(value ?? key).replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? name))
  } })
}))
const account = { id: 3, name: 'Owned', notes: null, platform: 'openai', type: 'apikey', status: 'inactive', read_only: false, proxy_id: null, expires_at: null, last_used_at: null, schedulable: false, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, created_at: '2026-10-30T01:00:00Z', updated_at: '' }
const proxy = { id: 7, name: 'Owned proxy', protocol: 'http', host: '8.8.8.8', port: 8080, username: 'u', has_password: true, status: 'inactive', created_at: '', updated_at: '' }
const page = (items: unknown[]) => ({ items, total: items.length, pages: 1, page: 1, page_size: 20 })
function render(component: typeof AccountsView | typeof ProxiesView | typeof UsageView) {
  return mount(component, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    CreateAccountModal: { name: 'CreateAccountModal', props: { show: Boolean, supplier: Boolean, proxies: Array, groups: Array }, template: '<div v-if="show" data-testid="shared-create" />' },
    Teleport: true,
    Pagination: true
  } } })
}

describe('supplier account copy', () => {
  it('uses the shorter owned-account name in both locales', () => {
    expect(en.supplier.accounts).toBe('My accounts')
    expect(en.supplier.accountFilterNotice).toContain('My accounts')
    expect(zh.supplier.accounts).toBe('我的账号')
    expect(zh.supplier.accountFilterNotice).toContain('“我的账号”')
  })
})
async function click(wrapper: ReturnType<typeof render>, name: string) {
  const button = wrapper.findAll('button').find(b => b.text() === name)
  expect(button, `button ${name}`).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

describe('supplier account editor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.accounts.list.mockResolvedValue(page([account]))
    api.proxies.list.mockResolvedValue(page([proxy]))
    api.accounts.update.mockResolvedValue(account)
    api.accounts.create.mockResolvedValue(account)
    api.accounts.getModels.mockResolvedValue([{ id: 'gpt-5', display_name: 'GPT-5' }])
  })
  function select(wrapper: ReturnType<typeof render>, id: string) {
    const control = wrapper.findAllComponents(Select).find(control => control.props('id') === id)
    expect(control, `Select ${id}`).toBeDefined()
    return control!
  }
  async function choose(wrapper: ReturnType<typeof render>, id: string, value: string) {
    const control = select(wrapper, id)
    const option = control.props('options').find(option => option.value === value)!
    expect(option).toBeDefined()
    await control.get('button').trigger('click')
    await control.findAll('[role="option"]').find(node => node.text() === option.label)!.trigger('click')
    await flushPromises()
  }

  it('retains untouched multiprotocol credentials and replaces explicitly selected endpoints', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'minimax' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    expect(select(wrapper, 'credential-base_url').props('options')).toContainEqual({
      value: 'https://api.minimax.io/anthropic', label: 'https://api.minimax.io/anthropic'
    })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenLastCalledWith(3, {})
    await click(wrapper, 'Edit')
    await choose(wrapper, 'credential-account_mode', 'coding')
    await choose(wrapper, 'credential-api_protocol', 'anthropic')
    await choose(wrapper, 'credential-base_url', 'https://api.minimax.io/anthropic')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenLastCalledWith(3, {credentials: {
      account_mode: 'coding', api_protocol: 'anthropic', base_url: 'https://api.minimax.io/anthropic'
    }})
    await click(wrapper, 'Edit')
    await choose(wrapper, 'credential-api_protocol', 'adaptive')
    for (const protocol of ['chat_completions', 'anthropic', 'responses']) {
      await choose(wrapper, 'credential-api_base_urls-' + protocol, 'https://api.minimax.io/' + (protocol === 'anthropic' ? 'anthropic' : 'v1'))
    }
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenLastCalledWith(3, {credentials: {
      api_protocol: 'adaptive', base_url: 'https://api.minimax.io/v1', api_base_urls: {chat_completions: 'https://api.minimax.io/v1', anthropic: 'https://api.minimax.io/anthropic', responses: 'https://api.minimax.io/v1'}
    }})
    wrapper.unmount()
  })

  it.each(['true', 'false'])('edits Bedrock force global as %s', async value => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'anthropic', type: 'bedrock' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await choose(wrapper, 'credential-aws_force_global', value)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenLastCalledWith(3, {credentials: {aws_force_global: value}})
    wrapper.unmount()
  })

  it.each([
    ['legacy-platform', 'legacy-type'], ['openai', 'legacy-type']
  ])('retains locked legacy selection %s / %s without rewriting it', async (platform, type) => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform, type }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    for (const [id, value] of [['supplier-account-platform', platform], ['supplier-account-type', type]]) {
      const control = select(wrapper, id)
      expect(control.props('options')).toContainEqual({ value, label: value })
      expect(control.props('modelValue')).toBe(value)
      expect(control.get('button').text()).toBe(value)
      expect(control.props('disabled')).toBe(true)
    }
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, {})
    wrapper.unmount()
  })

  it.each([
    ['', {}], ['0', { proxy_id: 0 }], ['7', { proxy_id: 7 }], ['19', { proxy_id: 19 }]
  ])('keeps proxy option values as strings and submits sparse numeric selection %s', async (value, changes) => {
    api.accounts.list.mockResolvedValue(page([{ ...account, proxy_id: 19 }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    const control = select(wrapper, 'supplier-account-proxy')
    expect(control.props('options').map(option => option.value)).toEqual(['', '0', '19', '7'])
    expect(control.props('options')).toContainEqual({ value: '19', label: '#19' })
    await choose(wrapper, 'supplier-account-proxy', value)
    expect(control.props('modelValue')).toBe(value)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, changes)
    wrapper.unmount()
  })

  it.each([
    ['openai', 'upstream', 'base_url', 'https://api.openai.com/v1'],
    ['anthropic', 'bedrock', 'auth_mode', 'apikey']
  ])('omits edited credential after explicitly choosing retain: %s / %s', async (platform, type, field, value) => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform, type }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await choose(wrapper, `credential-${field}`, value)
    await choose(wrapper, `credential-${field}`, '')
    expect(select(wrapper, `credential-${field}`).props('modelValue')).toBe('')
    expect(wrapper.get(`label[for="credential-${field}"]`).exists()).toBe(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, {})
    expect(api.showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('disables proxy selection until the proxy list finishes loading', async () => {
    let resolve!: (value: ReturnType<typeof page>) => void
    api.proxies.list.mockReturnValue(new Promise(done => { resolve = done }))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    expect(select(wrapper, 'supplier-account-proxy').props('disabled')).toBe(true)
    resolve(page([proxy]))
    await flushPromises()
    expect(select(wrapper, 'supplier-account-proxy').props('disabled')).toBe(false)
    wrapper.unmount()
  })

  it('sends name-only sparse edit without detaching an administrator proxy or erasing saved secrets', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="name"]').setValue('Renamed')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { name: 'Renamed' })
    expect(wrapper.text()).not.toContain('Delete')
  })
  it('rotates only the entered credential and leaves administrator endpoint intact', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="api_key"]').setValue('new-key')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { credentials: { api_key: 'new-key' } })
  })
  it('keeps Gemini Vertex Service Account JSON accounts on the JSON editor', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'gemini', type: 'service_account', auth_mode: 'service_account' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    expect(wrapper.find('[name="service_account_json"]').exists()).toBe(true)
    expect(wrapper.find('[name="project_id"]').exists()).toBe(true)
    expect(wrapper.find('[name="location"]').exists()).toBe(true)
    expect(wrapper.find('[name="api_key"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="AWS authentication mode"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('renders Gemini Vertex API key edits as write-only rotation without an auth-mode switch', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'gemini', type: 'service_account', auth_mode: 'apikey' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    const key = wrapper.get('[name="api_key"]')
    expect(key.attributes('type')).toBe('password')
    expect(key.attributes('autocomplete')).toBe('off')
    expect((key.element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[name="location"]').exists()).toBe(true)
    expect(wrapper.find('[name="service_account_json"]').exists()).toBe(false)
    expect(wrapper.find('[name="project_id"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="AWS authentication mode"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, {})
    wrapper.unmount()
  })
  it('rotates only entered Gemini Vertex API key fields', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'gemini', type: 'service_account', auth_mode: 'apikey' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="api_key"]').setValue('new-vertex-key')
    await wrapper.get('[name="location"]').setValue('global')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { credentials: { api_key: 'new-vertex-key', location: 'global' } })
    wrapper.unmount()
  })
  it('preserves RFC3339 expiry without converting an unchanged date or sending truncated seconds', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, expires_at: '2026-10-01T12:34:56+08:00' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    expect((wrapper.get('[name="expires_at"]').element as HTMLInputElement).value).toBe('2026-10-01T04:34')
    await wrapper.get('[name="name"]').setValue('Updated')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { name: 'Updated' })
  })
  it('clears an optional credential explicitly and ignores erased rotation input', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, type: 'oauth' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="access_token"]').setValue('new-token')
    await wrapper.get('[name="access_token"]').setValue('')
    const clear = wrapper.findAll('label').find(label => label.text().includes('Clear saved refresh_token'))
    await clear!.get('input').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { credentials: { refresh_token: '' } })
  })
  it('requires explicit proxy detach and expiry clearing', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await choose(wrapper, 'supplier-account-proxy', '0')
    await wrapper.get('[name="clear_expiry"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { proxy_id: 0, expires_at: 0 })
  })
  it('shows lifecycle and assignment explanation without supply controls', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, supplier_paused: true }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    expect(wrapper.text()).toContain('Inactive')
    expect(wrapper.text()).toContain('Administrators assign')
    for (const label of ['Pause supply', 'Resume supply', 'Not paused', 'Paused', '暂停供给', '恢复供给', '供应中', '已暂停']) {
      expect(wrapper.text()).not.toContain(label)
    }
    expect(wrapper.findAll('tbody button').map(button => button.attributes('aria-label'))).toEqual(['Test Connection', 'Edit'])
    const actual = await vi.importActual<typeof import('@/api/supplier')>('@/api/supplier')
    expect(actual.supplierAPI.accounts).not.toHaveProperty('pause')
  })
  it('uses the required account columns and formats account timestamps', async () => {
	vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-30T02:00:00Z'))
    api.accounts.list.mockResolvedValue(page([{ ...account, last_used_at: '2026-10-30T01:55:00Z' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    const headers = wrapper.findAll('thead th').map(node => node.text().trim())
    expect(headers).toEqual(['Name', 'Platform/Type', 'Status', 'Usage Windows', 'Test Connection', 'Last Used', 'Created', 'Actions'])
    expect(wrapper.text()).toContain('5m ago')
    expect(wrapper.text()).toContain('10/30/2026')
    expect(wrapper.findAll('button').some(button => button.text() === 'Edit')).toBe(true)
    expect(wrapper.findAll('button').some(button => button.text() === 'Pause supply')).toBe(false)
    vi.useRealTimers()
  })
  it('uses the admin status indicator without exposing error details', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-30T02:00:00Z'))
    api.accounts.list.mockResolvedValue(page([
      { ...account, id: 1, status: 'active', schedulable: true },
      { ...account, id: 2, status: 'error', schedulable: false, error_message: 'UPSTREAM_SECRET' },
      { ...account, id: 3, status: 'inactive', schedulable: false },
      { ...account, id: 4, status: 'active', schedulable: true, temp_unschedulable_until: '2026-10-30T03:00:00Z' },
      { ...account, id: 5, status: 'active', schedulable: true, rate_limit_reset_at: '2026-10-30T03:00:00Z' }
    ]))

    const wrapper = render(AccountsView)
    await flushPromises()

    expect(wrapper.text()).toContain(en.admin.accounts.status.active)
    expect(wrapper.text()).toContain(en.admin.accounts.status.error)
    expect(wrapper.text()).toContain(en.admin.accounts.status.inactive)
    expect(wrapper.text()).toContain(en.admin.accounts.status.tempUnschedulable)
    expect(wrapper.text()).toContain(en.admin.accounts.status.rateLimited)
    expect(wrapper.text()).not.toContain('UPSTREAM_SECRET')
    vi.useRealTimers()
  })
  it('shows an em dash when the account was never used', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    expect(wrapper.find('[data-testid="last-used-3"]').text()).toBe('—')
  })
  it('opens the restricted supplier connection test without admin model calls', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      body: { getReader: () => ({ read: vi.fn().mockResolvedValue({ done: true, value: undefined }) }) }
    }) as any
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Test Connection')
    expect(api.accounts.getModels).toHaveBeenCalledWith(3)
    expect(admin.getAvailableModels).not.toHaveBeenCalled()
    const modal = wrapper.findComponent({ name: 'AccountTestModal' })
    expect(modal.props('testPath')).toBe('/supplier/accounts/3/test')
    expect(modal.props('diagnosticOnly')).toBe(true)
    expect(wrapper.find('textarea').exists()).toBe(false)
  })
  it('disables all mutations on derived accounts', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, read_only: true }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    for (const label of ['Edit', 'Test Connection']) {
      expect(wrapper.get(`button[aria-label="${label}"]`).attributes('disabled')).toBeDefined()
    }
    expect(wrapper.text()).toContain('Derived account')
  })
  it('prevents duplicate create clicks while own proxies load', async () => {
    api.proxies.list.mockReturnValue(new Promise(() => {}))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Add account')
    const button = wrapper.findAll('button').find(button => button.text() === 'Add account')!
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(api.proxies.list).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('opens the shared supplier creation modal with owned and assigned proxy pages', async () => {
    api.proxies.list.mockImplementation((pageNumber: number) => Promise.resolve({
      ...page([pageNumber === 1 ? proxy : { id: 8, name: 'Assigned proxy', protocol: 'http', status: 'active', read_only: true, expires_at: null }]), total: 2, pages: 2, page: pageNumber
    }))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Add account')
    const modal = wrapper.findComponent({ name: 'CreateAccountModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('supplier')).toBe(true)
    expect(modal.props('groups')).toEqual([])
    expect(modal.props('proxies').map((p: { id: number }) => p.id)).toEqual([7, 8])
    expect(api.proxies.list).toHaveBeenNthCalledWith(1, 1)
    expect(api.proxies.list).toHaveBeenNthCalledWith(2, 2)
    expect(wrapper.find('#supplier-account-form').exists()).toBe(false)
  })

  it('shows the owned account usage windows without admin actions', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, platform: 'anthropic', type: 'oauth' }]))
    api.accounts.getUsage.mockResolvedValue({
      updated_at: null,
      five_hour: { utilization: 50, resets_at: null, remaining_seconds: 0 },
      seven_day: { utilization: 25, resets_at: null, remaining_seconds: 0 },
      seven_day_sonnet: null
    })

    const wrapper = render(AccountsView)
    await flushPromises()

    expect(api.accounts.getUsage).toHaveBeenCalledWith(3)
    expect(wrapper.text()).toContain('Usage Windows')
    expect(wrapper.text()).toContain('5h')
    expect(wrapper.findAll('button').some(button => button.text() === 'Query')).toBe(false)
    expect(admin.getUsage).not.toHaveBeenCalled()
    expect(admin.getClaudeResetCredits).not.toHaveBeenCalled()
  })

  it('does not query CN admin endpoints from the supplier account list', async () => {
    api.accounts.list.mockResolvedValue(page([
      { ...account, id: 4, platform: 'opencode_go', type: 'apikey' },
      { ...account, id: 5, platform: 'kimi', type: 'apikey' }
    ]))

    render(AccountsView)
    await flushPromises()

    expect(admin.queryQuota).not.toHaveBeenCalled()
    expect(admin.queryBalance).not.toHaveBeenCalled()
  })

})

describe('supplier proxy editor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.proxies.list.mockResolvedValue(page([proxy]))
    api.proxies.update.mockResolvedValue(proxy)
    api.proxies.test.mockResolvedValue({ reachable: true, latency_ms: 12 })
  })
  it('uses the shared protocol selector and saves the selected protocol', async () => {
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Edit')
    const selector = wrapper.findComponent(Select)
    expect(selector.exists()).toBe(true)
    expect(selector.props('options')).toEqual(['http', 'https', 'socks5', 'socks5h'].map(value => ({ value, label: value })))
    expect(selector.props('modelValue')).toBe('http')
    selector.vm.$emit('update:modelValue', 'socks5h')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.proxies.update).toHaveBeenCalledWith(7, { protocol: 'socks5h' })
    wrapper.unmount()
  })
  it('uses the shared lifecycle selector to save inactive status', async () => {
    api.proxies.list.mockResolvedValue(page([{ ...proxy, status: 'active' }]))
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Edit')
    const selector = wrapper.findAllComponents(Select).find(component => component.props('id') === 'supplier-proxy-status')
    expect(selector).toBeDefined()
    expect(selector!.props('modelValue')).toBe('active')
    expect(selector!.props('options')).toEqual([
      { value: 'active', label: en.common.active },
      { value: 'inactive', label: en.supplier.inactive }
    ])
    selector!.vm.$emit('update:modelValue', 'inactive')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.proxies.update).toHaveBeenCalledWith(7, { status: 'inactive' })
    wrapper.unmount()
  })
  it('preserves password and inactive status for a name-only edit', async () => {
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="name"]').setValue('Renamed proxy')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.proxies.update).toHaveBeenCalledWith(7, { name: 'Renamed proxy' })
    expect(wrapper.html()).not.toContain('password-value')
  })
  it('clears password only through an explicit control', async () => {
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Edit')
    await wrapper.get('[name="clear_password"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.proxies.update).toHaveBeenCalledWith(7, { password: '' })
  })
  it('labels testing as TCP reachability and reports only reachability and latency', async () => {
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Test TCP reachability')
    expect(api.proxies.test).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('12 ms')
    expect(wrapper.text()).toContain('does not verify proxy protocol')
  })
  it('keeps proxy in place on safe reference error', async () => {
    api.proxies.delete.mockRejectedValue({ reason: 'SUPPLIER_PROXY_IN_USE' })
    const wrapper = render(ProxiesView)
    await flushPromises()
    await click(wrapper, 'Delete')
    await click(wrapper, 'Confirm deletion')
    expect(api.showError).toHaveBeenCalled()
    expect(wrapper.text()).toContain('Owned proxy')
  })
})

describe('supplier usage', () => {
  function usageFixture(overrides: Partial<SupplierUsage> = {}) {
    return {
      timezone: 'UTC', ready: true, updated_at: '2026-10-31T12:30:00Z',
      cost: { total: '9007199254740993.1234567890123000', today: '0.00000000000000012300' },
      tokens: { total: 1234567, today: 123 }, requests: { total: 1200, today: 12 },
      accounts: { total: 3, enabled: 2, pending: 1, error: 0 },
      daily: Array.from({ length: 31 }, (_, i) => ({
        date: `2026-10-${String(i + 1).padStart(2, '0')}`, cost: '0.00000000000000012300', tokens: 123,
        enabled_accounts: i === 0 ? null : 2
      })),
      per_account: [
        { id: 9, name: 'Deleted account', status: 'deleted', deleted: true, today_cost: '0', last31_cost: '0.0000012300', total_cost: '9007199254740993.1234567890123000' },
        { id: 2, name: 'Pending account', status: 'pending', deleted: false, today_cost: '0.00000000000000012300', last31_cost: '1.2000', total_cost: '0' }
      ],
      ...overrides
    } satisfies SupplierUsage
  }
  beforeEach(() => {
    vi.clearAllMocks()
    api.usage.mockResolvedValue(usageFixture())
  })
  it.each([
    ['12480.3612345678', '$12,480.36'],
    ['0.005', '$0.01'],
    ['999.995', '$1,000.00'],
    ['0', '$0.00'],
    ['9007199254740993.1234567890123000', '$9,007,199,254,740,993.12'],
    ['0.00499999999999999999', '$0.00'],
    ['-0.005', '$-0.01'],
    ['-0.00499999999999999999', '$0.00']
  ])('rounds %s half-up without float arithmetic', (value, expected) => {
    expect(formatDecimalCurrency(value)).toBe(expected)
  })
  it('shows today prominently and totals below, retaining exact money in titles', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    const spend = wrapper.get('[data-testid="spend-card"]')
    expect(spend.get('.text-xl').text()).toBe('$0.00')
    expect(spend.get('.text-xl').attributes('title')).toBe('0.00000000000000012300')
    expect(spend.get('p.mt-1.text-xs').text()).toBe('Total: $9,007,199,254,740,993.12')
    expect(spend.get('p.mt-1.text-xs').attributes('title')).toBe('9007199254740993.1234567890123000')
    for (const [metric, today, total] of [['tokens', '123', '1,234,567'], ['requests', '12', '1,200']]) {
      const card = wrapper.get(`[data-testid="${metric}-card"]`)
      expect(card.get('.text-xl').text()).toBe(today)
      expect(card.get('p.mt-1.text-xs').text()).toBe('Total: ' + total)
    }
    expect(spend.text()).not.toContain('Today:')
    const cells = wrapper.findAll('tbody tr')[0]!.findAll('td')
    expect(cells.slice(2).map(cell => cell.text())).toEqual(['$0.00', '$1.20', '$0.00'])
    expect(cells[2]!.attributes('title')).toBe('0.00000000000000012300')
    expect(cells[3]!.attributes('title')).toBe('1.2000')
    expect(cells[4]!.attributes('title')).toBe('0')
    expect(api.usage).toHaveBeenCalledWith()
  })
  it('uses locale-specific total punctuation without changing the legend', () => {
    expect(en.supplier.usageStats.totalValue).toBe('Total: {value}')
    expect(zh.supplier.usageStats.totalValue).toBe('总计：{value}')
    expect([zh.supplier.usageStats.spend, zh.supplier.usageStats.tokens, zh.supplier.usageStats.enabled]).toEqual(['消费', 'Token', '启用'])
  })
  it('shows enabled, pending and error counts including zero', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    const accounts = wrapper.get('[data-testid="accounts-card"]')
    expect(accounts.text()).toContain('3')
    expect(accounts.text()).toContain('2 Enabled')
    expect(accounts.text()).toContain('1 Pending onboarding')
    expect(accounts.text()).toContain('0 Error')
  })
  it('renders per-account rows in id order with no rank or share column', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    expect(wrapper.findAll('thead th').map(node => node.text())).toEqual(['Accounts', 'Status', 'Today', 'Last 31 days', 'Total'])
    expect(wrapper.findAll('tbody tr').map(row => row.find('td').text())).toEqual(['#2 Pending account', '#9 Deleted account'])
    expect(wrapper.get('tbody').text()).toContain('$9,007,199,254,740,993.12')
  })
  it('labels deleted accounts and pending accounts', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows.find(row => row.text().includes('#9'))?.text()).toContain('Deleted')
    expect(rows.find(row => row.text().includes('#2'))?.text()).toContain('Pending onboarding')
  })
  it('passes null for enabled_accounts so the chart shows a gap', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    const chart = wrapper.findComponent({ name: 'Line' })
    expect(chart.exists()).toBe(true)
    const data = chart.props('data')
    expect(data.labels).toHaveLength(31)
    expect(data.datasets[2].data).toEqual([null, ...Array(30).fill(2)])
    expect(data.datasets[2].spanGaps).toBe(false)
  })
  it('uses monotone interpolation on all three datasets', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    const chart = wrapper.findComponent({ name: 'Line' })
    expect(chart.exists()).toBe(true)
    const datasets = chart.props('data').datasets
    expect(datasets).toHaveLength(3)
    expect(datasets.map((dataset: any) => dataset.cubicInterpolationMode)).toEqual(['monotone', 'monotone', 'monotone'])
    expect(datasets.map((dataset: any) => dataset.yAxisID)).toEqual(['cost', 'tokens', 'accounts'])
    const options = chart.props('options')
    expect(options.scales.cost.position).toBe('left')
    expect(options.scales.tokens.position).toBe('right')
    expect(options.scales.accounts.display).toBe(false)
    expect(options.scales.x.ticks.autoSkip).toBe(true)
    const label = options.plugins.tooltip.callbacks.label
    expect(label({ datasetIndex: 0, dataIndex: 1, dataset: datasets[0] })).toBe('Spend: $0.00')
    expect(label({ datasetIndex: 2, dataIndex: 1, dataset: datasets[2] })).toBe('Enabled: 2')
    expect(wrapper.text()).toContain('2026-10-31 12:30:00 UTC')
  })
  it.each([
    ['12480.3612345678', 'Spend: $12,480.36'],
    ['0.005', 'Spend: $0.01'],
    ['999.995', 'Spend: $1,000.00'],
    ['0', 'Spend: $0.00']
  ])('rounds chart tooltip %s and preserves its exact native title', async (cost, expected) => {
    const fixture = usageFixture()
    fixture.daily[0]!.cost = cost
    api.usage.mockResolvedValue(fixture)
    const wrapper = render(UsageView)
    await flushPromises()
    const chart = wrapper.findComponent({ name: 'Line' })
    const tooltip = chart.props('options').plugins.tooltip
    expect(tooltip.callbacks.label({ datasetIndex: 0, dataIndex: 0, dataset: chart.props('data').datasets[0] })).toBe(expected)
    const canvas = wrapper.get('canvas').element
    tooltip.external({ chart: { canvas }, tooltip: { opacity: 0, dataPoints: [{ dataIndex: 0 }], getActiveElements: () => [{ index: 0 }] } })
    expect(canvas.title).toBe(cost)
    tooltip.external({ chart: { canvas }, tooltip: { opacity: 1, dataPoints: [{ dataIndex: 0 }], getActiveElements: () => [] } })
    expect(canvas.title).toBe('')
  })
  it('shows the in-progress message when ready is false', async () => {
    api.usage.mockResolvedValue(usageFixture({ ready: false }))
    const wrapper = render(UsageView)
    await flushPromises()
    expect(wrapper.text()).toContain('Statistics in progress. Please come back later.')
    expect(zh.supplier.usageStats.inProgress).toBe('统计中，请稍后再来')
    expect(wrapper.find('[data-testid="spend-card"]').exists()).toBe(false)
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'Line' }).exists()).toBe(false)
    expect(wrapper.text()).not.toContain('9,007,199')
  })
  it('renders zeroes for a new supplier', async () => {
    api.usage.mockResolvedValue(usageFixture({
      cost: { total: '0.00000000', today: '0.00000000' },
      tokens: { total: 0, today: 0 }, requests: { total: 0, today: 0 },
      accounts: { total: 0, enabled: 0, pending: 0, error: 0 }, per_account: [], updated_at: null
    }))
    const wrapper = render(UsageView)
    await flushPromises()
    expect(wrapper.get('[data-testid="spend-card"]').text()).toContain('Total: $0.00')
    expect(wrapper.get('[data-testid="spend-card"]').get('.text-xl').text()).toBe('$0.00')
    for (const name of ['tokens', 'requests']) {
      expect(wrapper.get(`[data-testid="${name}-card"]`).text()).toContain('Total: 0')
      expect(wrapper.get(`[data-testid="${name}-card"]`).get('.text-xl').text()).toBe('0')
    }
    expect(wrapper.get('[data-testid="accounts-card"]').text()).toContain('0 Error')
    expect(wrapper.text()).not.toContain('Invalid Date')
    expect(wrapper.text()).toContain('No data found')
  })
  it('has no date picker or account filter', async () => {
    const wrapper = render(UsageView)
    await flushPromises()
    expect(wrapper.find('input[type="date"]').exists()).toBe(false)
    expect(wrapper.find('[name="account_id"]').exists()).toBe(false)
    expect(wrapper.find('form').exists()).toBe(false)
  })
})


describe('assigned supplier proxy', () => {
  it('renders the minimal DTO with read-only actions', async () => {
    api.proxies.list.mockResolvedValue(page([{ id: 19, name: 'Assigned', protocol: 'socks5', status: 'active', read_only: true, expires_at: null }]))
    const wrapper = render(ProxiesView)
    await flushPromises()
    expect(wrapper.text()).toContain('Administrator assigned · Read only')
    expect(wrapper.text()).toContain('socks5')
    expect(wrapper.text()).not.toContain('undefined')
    expect(wrapper.findAll('button').map(b => b.text())).not.toContain('Edit')
    expect(wrapper.findAll('button').map(b => b.text())).not.toContain('Delete')
    expect(wrapper.text()).toContain('Test TCP')
    expect(wrapper.text()).not.toContain('Password saved')
    api.proxies.test.mockResolvedValue({ reachable: true, latency_ms: 5 })
    await wrapper.findAll('button').find(button => button.text().includes('Test TCP'))!.trigger('click')
    await flushPromises()
    expect(api.proxies.test).toHaveBeenCalledWith(19)
  })
  it('preserves an assigned proxy when editing only the account name', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, proxy_id: 19 }]))
    api.proxies.list.mockResolvedValue(page([{ id: 19, name: 'Assigned', protocol: 'http', status: 'active', read_only: true, expires_at: null }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Edit')
    expect(wrapper.findAllComponents(Select).find(control => control.props('id') === 'supplier-account-proxy')!.props('options')).toContainEqual({ value: '19', label: 'Assigned · #19 · active' })
    await wrapper.get('[name="name"]').setValue('Renamed')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.accounts.update).toHaveBeenCalledWith(3, { name: 'Renamed' })
  })
})
