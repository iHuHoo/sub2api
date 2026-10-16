import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'
const api = vi.hoisted(() => ({ list: vi.fn(), users: vi.fn(), getUser: vi.fn(), assignSupplier: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: api.users, getById: api.getUser }, proxies: { getAllWithCount: vi.fn().mockResolvedValue([]), list: api.list, assignSupplier: api.assignSupplier, diagnosticsCapabilities: vi.fn().mockResolvedValue({ enabled: false, schema_version: 1 }) } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: api.showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
const proxy = { id: 9, name: 'Proxy', assigned_supplier_user_id: 12, assignment_valid: false }
const render = () => shallowMount(ProxiesView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="table" /></div>' },
  DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id"><slot name="cell-assignment" :row="row" /><slot name="cell-actions" :row="row" /></div>' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
} } })
let wrapper: ReturnType<typeof render>
beforeEach(() => { vi.clearAllMocks(); api.getUser.mockResolvedValue({ id: 12, username: 'Current supplier' }); api.list.mockResolvedValue({ items: [proxy], total: 1, pages: 1 }); api.users.mockResolvedValue({ items: [{ id: 20, username: 'supplier', email: 's@example.com' }], total: 1, pages: 1 }); api.assignSupplier.mockResolvedValue(proxy) })
afterEach(() => wrapper?.unmount())
async function open() { wrapper = render(); await flushPromises(); await wrapper.findAll('button').find(b => b.text() === 'supplier.assignProxy')!.trigger('click'); await flushPromises() }
describe('admin supplier assignment', () => {
  it('sends the original assignment and selected target', async () => {
    await open()
    expect(wrapper.text()).toContain('supplier.invalidAssignment')
    expect(api.users).toHaveBeenCalledWith(1, 20, { role: 'supplier', status: 'active', search: '' })
    await wrapper.get('[name="supplier_assignment"]').setValue('20')
    await wrapper.get('#proxy-assignment-form').trigger('submit'); await flushPromises()
    expect(api.assignSupplier).toHaveBeenCalledWith(9, { expected_supplier_user_id: 12, supplier_user_id: 20 })
  })
  it('loads the current supplier name on demand even outside active search results', async () => {
    await open()
    expect(api.getUser).toHaveBeenCalledWith(12, true)
    expect(wrapper.text()).toContain('Current supplier')
  })
  it('sends explicit null for a first assignment', async () => {
    api.list.mockResolvedValue({ items: [{ ...proxy, assigned_supplier_user_id: null }], total: 1, pages: 1 })
    await open()
    await wrapper.get('[name="supplier_assignment"]').setValue('20')
    await wrapper.get('#proxy-assignment-form').trigger('submit'); await flushPromises()
    expect(api.assignSupplier).toHaveBeenCalledWith(9, { expected_supplier_user_id: null, supplier_user_id: 20 })
  })
  it('revokes with null and retains dialog on conflict', async () => {
    api.assignSupplier.mockRejectedValue({ status: 409 })
    await open(); await wrapper.get('[name="supplier_assignment"]').setValue('')
    await wrapper.get('#proxy-assignment-form').trigger('submit'); await flushPromises()
    expect(api.assignSupplier).toHaveBeenCalledWith(9, { expected_supplier_user_id: 12, supplier_user_id: null })
    expect(wrapper.find('#proxy-assignment-form').exists()).toBe(true)
    expect(api.showError).toHaveBeenCalledWith('supplier.assignmentConflict')
  })
  it('offers search and subsequent pages', async () => {
    api.users.mockResolvedValue({ items: [], total: 40, pages: 2 })
    await open(); await wrapper.get('[data-testid="supplier-next"]').trigger('click'); await flushPromises()
    expect(api.users).toHaveBeenLastCalledWith(2, 20, { role: 'supplier', status: 'active', search: '' })
    await wrapper.get('[name="supplier_search"]').setValue('query')
    await wrapper.get('[data-testid="supplier-search"]').trigger('click'); await flushPromises()
    expect(api.users).toHaveBeenLastCalledWith(1, 20, { role: 'supplier', status: 'active', search: 'query' })
  })
  it.each(['20', ''])('Enter searches without committing selected supplier %j', async target => {
    await open()
    await wrapper.get('[name="supplier_assignment"]').setValue(target)
    const input = wrapper.get('[name="supplier_search"]')
    await input.setValue('another supplier')
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.element.dispatchEvent(enter)
    // jsdom has no native implicit form submission; emulate its uncancelled default.
    if (!enter.defaultPrevented) wrapper.get('form#proxy-assignment-form').element.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(api.assignSupplier).not.toHaveBeenCalled()
    expect(enter.defaultPrevented).toBe(true)
    expect(api.users).toHaveBeenLastCalledWith(1, 20, { role: 'supplier', status: 'active', search: 'another supplier' })
    expect(wrapper.find('#proxy-assignment-form').exists()).toBe(true)
    expect((wrapper.get('[name="supplier_assignment"]').element as HTMLSelectElement).value).toBe(target)
  })
  it('hides assignment for supplier-created proxies', async () => {
    api.list.mockResolvedValue({ items: [{ ...proxy, supplier_user_id: 8 }], total: 1, pages: 1 })
    wrapper = render(); await flushPromises()
    expect(wrapper.findAll('button').some(b => b.text() === 'supplier.assignProxy')).toBe(false)
  })
})
