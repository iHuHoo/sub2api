import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import AppSidebar from '../AppSidebar.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'

vi.mock('vue-router', async original => ({
  ...await original<typeof import('vue-router')>(),
  useRoute: () => ({ path: '/supplier/accounts' }),
  useRouter: () => ({ push: vi.fn() })
}))
vi.mock('vue-i18n', async original => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
describe('rendered supplier sidebar', () => {
  it.each([false, true])('renders only supplier navigation with backend mode %s', async (backendMode) => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const auth = useAuthStore()
    auth.user = { id: 5, role: 'supplier' } as never
    auth.token = 'synthetic-supplier'
    const app = useAppStore()
    app.cachedPublicSettings = { backend_mode_enabled: backendMode } as never
    const wrapper = mount(AppSidebar, { global: { plugins: [pinia], stubs: {
      RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      VersionBadge: true, Icon: true
    } } })
    await flushPromises()
    expect(wrapper.findAll('.sidebar-nav a').map(a => a.attributes('href'))).toEqual([
      '/supplier/accounts', '/supplier/proxies', '/supplier/usage', '/profile'
    ])
    wrapper.unmount()
  })
})
