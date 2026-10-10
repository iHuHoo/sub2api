import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en'
import AccountsView from '../AccountsView.vue'
import ProxiesView from '../ProxiesView.vue'
import UsageView from '../UsageView.vue'

const api = vi.hoisted(() => ({
  accounts: { list: vi.fn(), create: vi.fn(), update: vi.fn(), pause: vi.fn() },
  proxies: { list: vi.fn(), create: vi.fn(), update: vi.fn(), test: vi.fn(), delete: vi.fn() },
  usage: vi.fn(),
  showError: vi.fn(), showSuccess: vi.fn()
}))
vi.mock('@/api/supplier', () => ({ supplierAPI: api, supplierErrorKey: () => 'supplier.failed' }))
vi.mock('@/stores/app', () => ({ useAppStore: () => api }))
vi.mock('vue-i18n', async (original) => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, params: Record<string, unknown> = {}) => {
    const value = key.split('.').reduce((value: unknown, part) => (value as Record<string, unknown>)?.[part], en)
    return String(value ?? key).replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? name))
  } })
}))
const account = { id: 3, name: 'Owned', notes: null, platform: 'openai', type: 'apikey', status: 'inactive', supplier_paused: false, read_only: false, proxy_id: null, expires_at: null, created_at: '', updated_at: '' }
const proxy = { id: 7, name: 'Owned proxy', protocol: 'http', host: '8.8.8.8', port: 8080, username: 'u', has_password: true, status: 'inactive', created_at: '', updated_at: '' }
const page = (items: unknown[]) => ({ items, total: items.length, pages: 1, page: 1, page_size: 20 })
function render(component: typeof AccountsView | typeof ProxiesView | typeof UsageView) {
  return mount(component, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    Pagination: true
  } } })
}
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
    await wrapper.get('[name="proxy_id"]').setValue('0')
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
  it('disables all mutations on derived accounts', async () => {
    api.accounts.list.mockResolvedValue(page([{ ...account, read_only: true }]))
    const wrapper = render(AccountsView)
    await flushPromises()
    expect(wrapper.findAll('button').filter(b => ['Edit', 'Pause supply'].includes(b.text())).every(b => b.attributes('disabled') !== undefined)).toBe(true)
    expect(wrapper.text()).toContain('Derived account')
  })
  it('uses guided imports with official endpoints and never exposes administrator fields', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Add account')
    expect(wrapper.find('[name="api_key"]').exists()).toBe(true)
    expect(wrapper.find('[name="base_url"]').element.tagName).toBe('SELECT')
    expect(wrapper.find('[name="groups"]').exists()).toBe(false)
    expect(wrapper.find('[name="priority"]').exists()).toBe(false)
    expect(wrapper.find('[name="supplier_user_id"]').exists()).toBe(false)
    const platforms = wrapper.findAll('[name="platform"] option').map(o => o.attributes('value'))
    expect(platforms).not.toContain('composite')
    expect(platforms).toContain('antigravity')
  })
  it('imports OAuth, Bedrock and service-account fields explicitly', async () => {
    const wrapper = render(AccountsView)
    await flushPromises()
    await click(wrapper, 'Add account')
    await wrapper.get('[name="platform"]').setValue('anthropic')
    await wrapper.get('[name="type"]').setValue('bedrock')
    expect(wrapper.find('[name="aws_secret_access_key"]').exists()).toBe(true)
    await wrapper.get('[name="auth_mode"]').setValue('apikey')
    expect(wrapper.find('[name="api_key"]').exists()).toBe(true)
    expect(wrapper.find('[name="aws_secret_access_key"]').exists()).toBe(false)
    await wrapper.get('[name="type"]').setValue('service_account')
    expect(wrapper.get('[name="service_account_json"]').element.tagName).toBe('TEXTAREA')
    await wrapper.get('[name="platform"]').setValue('openai')
    await wrapper.get('[name="type"]').setValue('oauth')
    expect(wrapper.find('[name="chatgpt_account_id"]').exists()).toBe(true)
    expect(wrapper.find('[name="refresh_token"]').exists()).toBe(true)
    expect(wrapper.find('[name="base_url"]').exists()).toBe(false)
  })
})

describe('supplier proxy editor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.proxies.list.mockResolvedValue(page([proxy]))
    api.proxies.update.mockResolvedValue(proxy)
    api.proxies.test.mockResolvedValue({ reachable: true, latency_ms: 12 })
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
