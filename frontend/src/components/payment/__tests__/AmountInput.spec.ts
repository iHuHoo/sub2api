import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AmountInput from '../AmountInput.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('AmountInput payment currency', () => {
  it('shows the configured currency symbol and only the supplied quick amounts', () => {
    const wrapper = mount(AmountInput, {
      props: {
        modelValue: null,
        amounts: [100, 200, 500],
        currencySymbol: '¥',
      },
    })

    expect(wrapper.text()).toContain('¥')
    expect(wrapper.findAll('button').map(button => button.text())).toEqual(['100', '200', '500'])
    expect(wrapper.text()).not.toContain('$')
  })
})
