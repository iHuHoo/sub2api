import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'
import complete from '@/components/proxy/__tests__/fixtures/complete.json'

const api = vi.hoisted(() => ({ list: vi.fn(), getAllWithCount: vi.fn(), diagnosticsCapabilities: vi.fn(), checkDiagnostics: vi.fn(), getDiagnostics: vi.fn(), checkProxyQuality: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: api } }))
vi.mock('@/api/admin/proxies', () => ({ proxiesAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id"><slot name="cell-latency" :row="row" /><slot name="cell-actions" :row="row" /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    Select: { props: ['modelValue', 'options'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :value="option.value">{{ option.label }}</option></select>' }
  } }
})
let wrapper: ReturnType<typeof mountView>
const button = (key: string) => wrapper.findAll('button').find(b => b.text() === key)!
beforeEach(() => {
  vi.resetAllMocks()
  api.list.mockResolvedValue({ items: [{ id: 1, name: 'test proxy', quality_checked: 1, quality_score: 99, quality_grade: 'A' }], total: 1, pages: 1 })
  api.getAllWithCount.mockResolvedValue([])
  api.diagnosticsCapabilities.mockResolvedValue({ enabled: true, schema_version: 1, modes: ['quick', 'sample'] })
  api.checkDiagnostics.mockResolvedValue({ report: complete, saved: false })
})
afterEach(() => wrapper?.unmount())
describe('proxy diagnostics integration', () => {
  it('rejects a paginated list cancelled between pages instead of returning partial targets', async () => {
    wrapper = mountView(); await flushPromises()
    const controller = new AbortController()
    api.list.mockImplementationOnce(async () => {
      controller.abort()
      return { items: [{ id: 1 }], pages: 2 }
    }).mockRejectedValueOnce({ code: 'ERR_CANCELED' })
    const vm = wrapper.vm as unknown as { fetchAllProxiesForBatch: (signal: AbortSignal) => Promise<unknown> }
    await expect(vm.fetchAllProxiesForBatch(controller.signal)).rejects.toMatchObject({ code: 'ERR_CANCELED' })
    expect(api.checkDiagnostics).not.toHaveBeenCalled()
  })
  it('uses the selected sample mode and hides legacy scores', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('#diagnostics-mode').setValue('sample')
    await button('admin.proxies.diagnostics.run').trigger('click'); await flushPromises()
    expect(api.checkDiagnostics).toHaveBeenCalledWith(1, 'sample', { signal: expect.any(AbortSignal) })
    expect(api.checkProxyQuality).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'ProxyDiagnosticsReport' }).props('saved')).toBe(false)
    expect(wrapper.text()).not.toContain('admin.proxies.qualityInline')
  })
  it('retains legacy checks when explicitly disabled', async () => {
    api.diagnosticsCapabilities.mockResolvedValue({ enabled: false })
    api.checkProxyQuality.mockResolvedValue({ items: [], score: 99, grade: 'A' })
    wrapper = mountView(); await flushPromises()
    await button('admin.proxies.qualityCheck').trigger('click'); await flushPromises()
    expect(api.checkProxyQuality).toHaveBeenCalledWith(1)
    expect(api.checkDiagnostics).not.toHaveBeenCalled()
    expect(wrapper.find('#diagnostics-mode').exists()).toBe(false)
  })
  it('does not silently fall back when capability discovery fails', async () => {
    api.diagnosticsCapabilities.mockRejectedValue({ status: 503 })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('admin.proxies.diagnostics.capabilitiesFailed')
    expect(button('admin.proxies.qualityCheck').attributes('disabled')).toBeDefined()
    expect(api.checkProxyQuality).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('admin.proxies.qualityInline')
  })
  it('hides legacy scores while capabilities are pending', async () => {
    api.diagnosticsCapabilities.mockImplementation(() => new Promise(() => {}))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).not.toContain('admin.proxies.qualityInline')
  })
  it('shows legacy scores only after capabilities explicitly disable diagnostics', async () => {
    api.diagnosticsCapabilities.mockResolvedValue({ enabled: false })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('admin.proxies.qualityInline')
  })
  it('explains unsupported capabilities schema explicitly', async () => {
    api.diagnosticsCapabilities.mockRejectedValue({ reason: 'schema_unsupported' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('admin.proxies.diagnostics.errors.schema_unsupported')
    expect(api.checkProxyQuality).not.toHaveBeenCalled()
  })
  it('retrieves a recent report and displays a fixed missing-report error', async () => {
    api.getDiagnostics.mockRejectedValue({ status: 404, message: 'credential-do-not-show' })
    wrapper = mountView(); await flushPromises()
    await button('admin.proxies.diagnostics.latest').trigger('click'); await flushPromises()
    expect(api.getDiagnostics).toHaveBeenCalledWith(1, { signal: expect.any(AbortSignal) })
    expect(wrapper.text()).toContain('admin.proxies.diagnostics.errors.not_found')
    expect(wrapper.text()).not.toContain('credential-do-not-show')
  })
})
