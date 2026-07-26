import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import VersionBadge from '../VersionBadge.vue'

const performUpdate = vi.hoisted(() => vi.fn())
const getRollbackVersions = vi.hoisted(() => vi.fn())
const rollbackAPI = vi.hoisted(() => vi.fn())
const appStore = vi.hoisted(() => ({
  versionLoading: false,
  currentVersion: '0.1.165-aox.0.0.1',
  latestVersion: '0.1.165-aox.0.0.1',
  hasUpdate: false,
  releaseInfo: null,
  buildType: 'aox',
  fetchVersion: vi.fn().mockResolvedValue(undefined),
  clearVersionCache: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isAdmin: true }),
  useAppStore: () => appStore,
}))

vi.mock('@/api/admin/system', () => ({
  performUpdate,
  restartService: vi.fn(),
  getRollbackVersions,
  rollback: rollbackAPI,
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copied: false,
    copyToClipboard: vi.fn(),
  }),
}))

describe('VersionBadge Aox managed build', () => {
  it('shows managed status without update or rollback actions', async () => {
    const wrapper = shallowMount(VersionBadge, {
      global: {
        stubs: {
          Transition: false,
        },
      },
    })
    await flushPromises()
    await wrapper.find('button').trigger('click')

    expect(wrapper.text()).toContain('v0.1.165-aox.0.0.1')
    expect(wrapper.text()).toContain('version.aoxManaged')
    expect(wrapper.text()).not.toContain('version.updateNow')
    expect(wrapper.text()).not.toContain('version.rollback')
    expect(wrapper.find('button[title="version.refresh"]').exists()).toBe(false)
    expect(performUpdate).not.toHaveBeenCalled()
    expect(getRollbackVersions).not.toHaveBeenCalled()
    expect(rollbackAPI).not.toHaveBeenCalled()
  })
})
