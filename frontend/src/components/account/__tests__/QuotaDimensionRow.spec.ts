import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import QuotaDimensionRow from '../QuotaDimensionRow.vue'
import Select from '@/components/common/Select.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const props = {
  dim: 'weekly' as const, label: 'Weekly', limit: 10, quotaNotifyGlobalEnabled: false,
  notifyEnabled: null, notifyThreshold: null, notifyThresholdType: null,
  resetMode: 'fixed' as const, resetHour: null, resetDay: null, resetTimezone: null,
  hintRolling: '', hintFixed: '', hourOptions: [0, 12],
  dayOptions: [{ value: 1, key: 'monday' }, { value: 5, key: 'friday' }], timezoneOptions: ['UTC'],
}

describe('QuotaDimensionRow select contracts', () => {
  it('emits numeric day/hour values from actual options, including zero', async () => {
    const wrapper = mount(QuotaDimensionRow, { props, global: { stubs: { Teleport: true } } })
    const selects = wrapper.findAllComponents(Select)
    expect(selects).toHaveLength(4)
    for (const [index, label, event, value] of [
      [1, 'admin.accounts.dayOfWeek.friday', 'update:resetDay', 5],
      [2, '12:00', 'update:resetHour', 12],
      [2, '00:00', 'update:resetHour', 0],
    ] as const) {
      await selects[index].get('button').trigger('click')
      await selects[index].findAll('[role="option"]').find(option => option.text() === label)!.trigger('click')
      expect(wrapper.emitted(event)?.at(-1)).toEqual([value])
    }
    wrapper.unmount()
  })

  it('initializes missing fixed reset values with numeric defaults', async () => {
    const wrapper = mount(QuotaDimensionRow, { props: { ...props, resetMode: 'rolling' }, global: { stubs: { Teleport: true } } })
    const mode = wrapper.getComponent(Select)
    await mode.get('button').trigger('click')
    await mode.findAll('[role="option"]').find(option => option.text() === 'admin.accounts.quotaResetModeFixed')!.trigger('click')
    expect(wrapper.emitted('update:resetMode')).toEqual([['fixed']])
    expect(wrapper.emitted('update:resetDay')).toEqual([[1]])
    expect(wrapper.emitted('update:resetHour')).toEqual([[0]])
    expect(wrapper.emitted('update:resetTimezone')).toEqual([['UTC']])
    wrapper.unmount()
  })
})
