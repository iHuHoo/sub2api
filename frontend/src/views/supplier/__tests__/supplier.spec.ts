import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import AccountsView from '../AccountsView.vue'
import ProxiesView from '../ProxiesView.vue'
import UsageView from '../UsageView.vue'

const api = vi.hoisted(() => ({
  accounts: { list: vi.fn(), create: vi.fn(), update: vi.fn(), pause: vi.fn(), getUsage: vi.fn(), getModels: vi.fn() },
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
const account = { id: 3, name: 'Owned', notes: null, platform: 'openai', type: 'apikey', status: 'inactive', supplier_paused: false, read_only: false, proxy_id: null, expires_at: null, last_used_at: null, created_at: '2026-10-30T01:00:00Z', updated_at: '' }
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
    api.accounts.pause.mockResolvedValue(account)
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
  it('shows lifecycle and assignment explanation without inferring assignment; resume only clears supplier pause', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, supplier_paused: true }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    expect(wrapper.text()).toContain('Inactive')
    expect(wrapper.text()).toContain('Administrators assign')
    await click(wrapper, 'Resume supply')
    expect(api.accounts.pause).toHaveBeenCalledWith(3, false)
  })
  it('uses the required account columns and formats account timestamps', async () => {
	vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-30T02:00:00Z'))
    api.accounts.list.mockResolvedValue(page([{ ...account, last_used_at: '2026-10-30T01:55:00Z' }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    const headers = wrapper.findAll('thead th').map(node => node.text().trim())
    expect(headers).toEqual(['Name', 'Platform/Type', 'Status', 'Usage Windows', 'Test Connection', 'Last Used', 'Created'])
    expect(wrapper.text()).toContain('5m ago')
    expect(wrapper.text()).toContain('10/30/2026')
    expect(wrapper.findAll('button').some(button => button.text() === 'Edit')).toBe(true)
    expect(wrapper.findAll('button').some(button => button.text() === 'Pause supply')).toBe(true)
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
    expect(wrapper.find('textarea').exists()).toBe(false)
  })
  it('disables all mutations on derived accounts', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, read_only: true }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    expect(wrapper.findAll('button').filter(b => ['Edit', 'Pause supply'].includes(b.text())).every(b => b.attributes('disabled') !== undefined)).toBe(true)
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
  it('rejects reversed/overlong ranges and nonpositive IDs before requesting statistics', async () => {
    api.usage.mockResolvedValue({ timezone: 'UTC', summary: { requests: 0, input_tokens: 0, output_tokens: 0, cache_creation_tokens: 0, cache_read_tokens: 0 }, daily: [] })
    const wrapper = render(UsageView)
    await flushPromises()
    api.usage.mockClear()
    for (const [start, end, id] of [['2026-10-02', '2026-10-01', ''], ['2025-01-01', '2026-01-02', ''], ['2026-10-01', '2026-10-02', '0']]) {
      await wrapper.get('[name="start_date"]').setValue(start)
      await wrapper.get('[name="end_date"]').setValue(end)
      await wrapper.get('[name="account_id"]').setValue(id)
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(api.usage).not.toHaveBeenCalled()
    }
  })
  it('requests inclusive UTC dates and exact account filter; renders token-only totals', async () => {
    api.usage.mockResolvedValue({ timezone: 'UTC', summary: { requests: 2, input_tokens: 20, output_tokens: 40, cache_creation_tokens: 60, cache_read_tokens: 80 }, daily: [{ date: '2026-10-01', requests: 2, input_tokens: 20, output_tokens: 40, cache_creation_tokens: 60, cache_read_tokens: 80 }] })
    const wrapper = render(UsageView)
    await flushPromises()
    await wrapper.get('[name="start_date"]').setValue('2026-10-01')
    await wrapper.get('[name="end_date"]').setValue('2026-10-31')
    await wrapper.get('[name="account_id"]').setValue('3')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.usage).toHaveBeenLastCalledWith({ start_date: '2026-10-01', end_date: '2026-10-31', account_id: 3 })
    expect(wrapper.text()).toContain('UTC')
    expect(wrapper.text()).toContain('2026-10-01')
    expect(wrapper.text()).toContain('Calls recorded in usage records')
    expect(wrapper.text()).not.toContain('$')
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
