import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import Select from '@/components/common/Select.vue'
import ClashSubscriptionDialog from '../ClashSubscriptionDialog.vue'

const api = vi.hoisted(() => ({ metadata: vi.fn(), create: vi.fn(), reset: vi.fn(), revoke: vi.fn() }))
vi.mock('@/api/clashSubscription', () => ({ clashSubscriptionAPI: () => api }))
vi.mock('@/api', () => ({ totpAPI: { stepUp: vi.fn() } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const metadata = { exists: false, active: false, version: 0, expires_at: null, revoked_at: null, exportable_count: 1, skipped_count: 0 }
const mountDialog = (role: "admin" | "supplier" = "supplier") => mount(ClashSubscriptionDialog, { props: { show: true, role }, global: { stubs: { TotpStepUpDialog: { props: ["controller"], template: `<div v-if="controller.visible.value"><button data-test="verified" @click="controller.onVerified()">Verify</button><button data-test="cancel-step" @click="controller.onCancel()">Cancel</button></div>` }, BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' } } } })
describe('ClashSubscriptionDialog', () => {
 beforeEach(() => { vi.resetAllMocks(); api.metadata.mockResolvedValue({ ...metadata }) })
 it('names the duration selector with its translated label', async () => {
  const wrapper = mountDialog(); await flushPromises()
  expect(wrapper.getComponent(Select).get('button').attributes('aria-label')).toBe('clash.duration')
  wrapper.unmount()
 })
 for (const role of ['admin', 'supplier'] as const) {
  for (const action of ['create', 'reset'] as const) {
   it(`${role} ${action}: verifies but waits for explicit retry`, async () => {
    if (action === 'reset') api.metadata.mockResolvedValue({ ...metadata, exists: true, version: 7 })
    api[action].mockRejectedValueOnce({ reason: 'STEP_UP_REQUIRED' }).mockResolvedValue({ metadata: { ...metadata, exists: true, version: 8 }, subscription_url: 'https://api.example/?token=fixture' })
    const wrapper = mountDialog(role); await flushPromises()
    const click = async (key: string) => { await wrapper.findAll('button').find(b => b.text() === key)!.trigger('click'); await flushPromises() }
    await click(`clash.${action}`)
    if (action === 'reset') await click('clash.proceed')
    expect(wrapper.find('[data-test="verified"]').exists()).toBe(true)
    await wrapper.get('[data-test="verified"]').trigger('click'); await flushPromises()
    expect(api[action]).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-test="link"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('clash.stepUpRetry')
    await click(`clash.${action}`)
    if (action === 'reset') await click('clash.proceed')
    expect(api[action]).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="link"]').exists()).toBe(true)
    wrapper.unmount()
   })
  }
  it(`${role}: close cancels pending verification without a mutation`, async () => {
   api.create.mockRejectedValue({ code: 'STEP_UP_REQUIRED' })
   const wrapper = mountDialog(role); await flushPromises()
   await wrapper.get('[data-test="create"]').trigger('click'); await flushPromises()
   expect(wrapper.find('[data-test="verified"]').exists()).toBe(true)
   await wrapper.setProps({ show: false }); await flushPromises()
   expect(wrapper.find('[data-test="verified"]').exists()).toBe(false)
   expect(api.create).toHaveBeenCalledTimes(1)
   wrapper.unmount()
  })
  it(`${role}: directs users without TOTP to profile, without retrying`, async () => {
   api.create.mockRejectedValue({ code: 'STEP_UP_TOTP_NOT_ENABLED' })
   const wrapper = mountDialog(role); await flushPromises()
   await wrapper.get('[data-test="create"]').trigger('click'); await flushPromises()
   expect(wrapper.text()).toContain('clash.stepUpSetup')
   expect(wrapper.find('[data-test="verified"]').exists()).toBe(false)
   expect(api.create).toHaveBeenCalledTimes(1)
   wrapper.unmount()
  })
 }
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
