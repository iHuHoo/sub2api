import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import Select from '../Select.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
let wrapper: VueWrapper
const options = [
  { value: 'group-us', label: 'US regions', kind: 'group', disabled: true },
  { value: 'us-east-1', label: 'US East' },
  { value: 'group-eu', label: 'Europe regions', kind: 'group', disabled: true },
  { value: 'eu-west-1', label: 'Europe West' },
]
const open = async () => {
  wrapper = mount(Select, { props: { modelValue: '', options, searchable: true } })
  await wrapper.get('button').trigger('click')
  await nextTick()
  return document.body.querySelector<HTMLElement>('[role="listbox"]')!
}
afterEach(() => { wrapper?.unmount(); document.body.innerHTML = '' })
describe('Select grouped region options', () => {
  it('does not select group headings by click, including search matches', async () => {
    const list = await open()
    const heading = list.querySelector<HTMLElement>('.select-option-group')!
    expect(heading.getAttribute('aria-disabled')).toBe('true')
    heading.click()
    await nextTick()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    const search = list.querySelector<HTMLInputElement>('input')!
    search.value = 'regions'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(list.querySelectorAll('[role="option"]')).toHaveLength(2)
    for (const item of list.querySelectorAll<HTMLElement>('[role="option"]')) {
      expect(item.getAttribute('aria-disabled')).toBe('true')
      item.click()
    }
    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
  it('skips headings with the keyboard and emits the region string', async () => {
    const list = await open()
    expect(list.querySelector('.select-option-focused')?.textContent).toContain('US East')
    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await nextTick()
    expect(list.querySelector('.select-option-focused')?.textContent).toContain('Europe West')
    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(wrapper.emitted('update:modelValue')).toEqual([['eu-west-1']])
  })
})
