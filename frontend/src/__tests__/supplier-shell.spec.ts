import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'

const harness = vi.hoisted(() => ({
  auth: { isAuthenticated: true, isAdmin: false, isSupplier: true, user: { id: 5 } },
  subscriptionEnabled: true,
  subscription: { fetchActiveSubscriptions: vi.fn(), startPolling: vi.fn(), clear: vi.fn() },
  announcements: { fetchAnnouncements: vi.fn(), reset: vi.fn() },
  compliance: { fetchStatus: vi.fn(), reset: vi.fn(), requireAcknowledgement: vi.fn() },
  afterEach: null as null | (() => void)
}))
vi.mock('@/stores', () => ({
  useAuthStore: () => reactive(harness.auth),
  useAppStore: () => ({ siteName: 'test', siteLogo: '', fetchPublicSettings: vi.fn().mockResolvedValue({}) }),
  useSubscriptionStore: () => harness.subscription,
  useAnnouncementStore: () => harness.announcements,
  useAdminComplianceStore: () => harness.compliance,
  useAdminSettingsStore: () => ({ customMenuItems: [] })
}))
vi.mock('vue-router', async original => ({
  ...await original<typeof import('vue-router')>(),
  useRouter: () => ({ afterEach: (callback: () => void) => { harness.afterEach = callback }, replace: vi.fn() }),
  useRoute: () => ({ fullPath: '/supplier/accounts', path: '/supplier/accounts', meta: {} })
}))
vi.mock('@/api/setup', () => ({ getSetupStatus: vi.fn().mockResolvedValue({ needs_setup: false }) }))
vi.mock('@/utils/featureFlags', async original => ({ ...await original<typeof import('@/utils/featureFlags')>(), isFeatureFlagEnabled: () => reactive(harness).subscriptionEnabled }))
vi.mock('@/router/title', () => ({ resolveRouteDocumentTitle: () => 'test' }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
function render() {
  return mount(App, { global: { stubs: { RouterView: true, Toast: true, NavigationProgress: true, AnnouncementPopup: true, AdminComplianceDialog: true } } })
}
describe('supplier background isolation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    Object.assign(reactive(harness.auth), { isAuthenticated: true, isAdmin: false, isSupplier: true })
    harness.subscription.fetchActiveSubscriptions.mockResolvedValue([])
  })
  afterEach(() => { vi.useRealTimers() })
  it('makes no subscription or announcement calls on restore, visibility, route changes or flag changes', async () => {
    const wrapper = render()
    await flushPromises()
    document.dispatchEvent(new Event('visibilitychange'))
    harness.afterEach?.()
    reactive(harness).subscriptionEnabled = false
    await flushPromises()
    reactive(harness).subscriptionEnabled = true
    await flushPromises()
    expect(harness.subscription.fetchActiveSubscriptions).not.toHaveBeenCalled()
    expect(harness.subscription.startPolling).not.toHaveBeenCalled()
    expect(harness.announcements.fetchAnnouncements).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'AnnouncementPopup' }).exists()).toBe(false)
    wrapper.unmount()
  })
  it('clears stale consumer stores and suppresses a delayed login fetch on switching to supplier', async () => {
    const auth = reactive(harness.auth)
    auth.isAuthenticated = false
    auth.isSupplier = false
    const wrapper = render()
    await flushPromises()
    auth.isAuthenticated = true
    await flushPromises()
    expect(harness.subscription.startPolling).toHaveBeenCalled()
    harness.subscription.clear.mockClear()
    harness.announcements.reset.mockClear()
    auth.isSupplier = true
    await flushPromises()
    expect(harness.subscription.clear).toHaveBeenCalled()
    expect(harness.announcements.reset).toHaveBeenCalled()
    harness.announcements.fetchAnnouncements.mockClear()
    await vi.advanceTimersByTimeAsync(3000)
    harness.afterEach?.()
    document.dispatchEvent(new Event('visibilitychange'))
    expect(harness.announcements.fetchAnnouncements).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
