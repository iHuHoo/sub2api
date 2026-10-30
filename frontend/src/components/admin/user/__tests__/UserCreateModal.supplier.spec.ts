import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import UserCreateModal from '../UserCreateModal.vue'
const create = vi.hoisted(() => vi.fn().mockResolvedValue({}))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { create } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: (action: () => unknown) => action() }), isStepUpCancelled: () => false, isStepUpBlocked: () => false, stepUpBlockReason: () => '' }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
it('creates a supplier through the administrator user form', async () => {
  const wrapper = mount(UserCreateModal, { props: { show: true }, global: { stubs: { Teleport: true, BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Icon: true, TotpStepUpDialog: true } } })
  const select = wrapper.getComponent(Select)
  expect(select.props('options')).toContainEqual({ value: 'supplier', label: 'supplier.role' })
  await select.get('button').trigger('click')
  await select.findAll('[role="option"]').find(option => option.text() === 'supplier.role')!.trigger('click')
  await wrapper.get('input[type="email"]').setValue('supply@example.test')
  await wrapper.get('input[type="text"]').setValue('password')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ role: 'supplier', email: 'supply@example.test' }))
})
