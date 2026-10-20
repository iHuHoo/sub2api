import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import ClashSubscriptionDialog from '../ClashSubscriptionDialog.vue'

const api = vi.hoisted(() => ({ metadata: vi.fn(), create: vi.fn(), reset: vi.fn(), revoke: vi.fn() }))
vi.mock('@/api/clashSubscription', () => ({ clashSubscriptionAPI: () => api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const metadata = { exists: false, active: false, version: 0, expires_at: null, revoked_at: null, exportable_count: 1, skipped_count: 0 }
const mountDialog = () => mount(ClashSubscriptionDialog, { props: { show: true, role: 'supplier' }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' } } } })
describe('ClashSubscriptionDialog', () => {
 beforeEach(() => { vi.resetAllMocks(); api.metadata.mockResolvedValue({ ...metadata }) })
 it('disables creation with zero exportable nodes', async () => {
  api.metadata.mockResolvedValue({ ...metadata, exportable_count: 0 })
  const wrapper = mountDialog(); await flushPromises()
  expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeDefined()
  wrapper.unmount()
 })
 it('discards a grant arriving after close', async () => {
  let resolve!: (value: unknown) => void
  api.create.mockReturnValue(new Promise(r => { resolve = r }))
  const wrapper = mountDialog(); await flushPromises(); await wrapper.get('[data-test="create"]').trigger('click'); await flushPromises()
  await wrapper.setProps({ show: false })
  resolve({ metadata: { ...metadata, exists: true, active: true, version: 1 }, subscription_url: 'https://api.example/?token=fixture' })
  await flushPromises(); expect(wrapper.find('[data-test="link"]').exists()).toBe(false)
  wrapper.unmount()
 })
 it('keeps revoke available for an existing subscription with zero nodes', async () => {
  api.metadata.mockResolvedValue({ ...metadata, exists: true, active: true, version: 3, exportable_count: 0 })
  const wrapper = mountDialog(); await flushPromises()
  const buttons = wrapper.findAll('button')
  expect(buttons.find(b => b.text() === 'clash.reset')?.attributes('disabled')).toBeDefined()
  expect(buttons.find(b => b.text() === 'clash.revoke')?.attributes('disabled')).toBeUndefined()
  expect(wrapper.find('[data-test="link"]').exists()).toBe(false)
  wrapper.unmount()
 })
 it('does not retry a creation after a conflict or lost response', async () => {
  api.create.mockRejectedValue(new Error('conflict'))
  const wrapper = mountDialog(); await flushPromises(); await wrapper.get('[data-test="create"]').trigger('click'); await flushPromises()
  expect(api.create).toHaveBeenCalledTimes(1); expect(api.reset).not.toHaveBeenCalled()
  expect(wrapper.text()).toContain('clash.retry'); expect(wrapper.find('[data-test="link"]').exists()).toBe(false)
  wrapper.unmount()
 })
 it('disables management when feature metadata is unavailable', async () => {
  api.metadata.mockRejectedValue(new Error('disabled'))
  const wrapper = mountDialog(); await flushPromises()
  expect(wrapper.text()).toContain('clash.unavailable'); expect(wrapper.find('[data-test="create"]').exists()).toBe(false)
  wrapper.unmount()
 })
 it('rechecks node count before creation', async () => {
  const wrapper = mountDialog(); await flushPromises()
  api.metadata.mockResolvedValue({ ...metadata, exportable_count: 0 })
  await wrapper.get('[data-test="create"]').trigger('click'); await flushPromises()
  expect(api.create).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('clash.zero')
  wrapper.unmount()
 })
})
