import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import BatchImageGuideView from '../BatchImageGuideView.vue'
import Select from '@/components/common/Select.vue'

const { submitBatchImageJob } = vi.hoisted(() => ({ submitBatchImageJob: vi.fn().mockRejectedValue(new Error('test stop after request')) }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ locale: { value: 'en' }, t: (key: string, args?: { n?: number }) => args?.n ? `${key} ${args.n}` : key }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ fetchPublicSettings: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/api', () => ({ keysAPI: { list: vi.fn().mockResolvedValue({ items: [7, 9].map(id => ({ id, name: `Key ${id}`, key: `test-key-${id}`, status: 'active', group: { platform: 'gemini', name: 'Gemini', allow_batch_image_generation: true } })) }) } }))
vi.mock('@/api/batchImage', () => ({
  submitBatchImageJob,
  listBatchImageJobs: vi.fn().mockResolvedValue({ data: [], has_more: false }),
  listBatchImageModels: vi.fn().mockResolvedValue({ data: [{ id: 'gemini-test-image' }] }),
}))

afterEach(() => vi.clearAllMocks())

describe('BatchImageGuideView create selectors', () => {
  it('keeps API key and output count numeric and submits the selected values', async () => {
    const wrapper = mount(BatchImageGuideView, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /></div>' },
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
      Icon: true, Teleport: true, 'i18n-t': true,
    } } })
    try {
      await flushPromises()
      await wrapper.findAll('button').find(button => button.text() === 'batchImage.actions.createJob')!.trigger('click')
      const form = wrapper.get('form')
      const [key, model, format, count] = form.findAllComponents(Select)
      expect(key.props('options')[0]).toEqual({ value: 0, label: 'batchImage.create.selectKeyPlaceholder' })
      await key.get('button').trigger('click')
      await key.findAll('[role="option"]').find(option => option.text() === 'Key 9 · Gemini')!.trigger('click')
      await flushPromises()
      expect(key.props('modelValue')).toBe(9)
      expect(model.props('modelValue')).toBe('gemini-test-image')
      await count.get('button').trigger('click')
      await count.findAll('[role="option"]').find(option => option.text() === 'batchImage.create.outputCountOption 3')!.trigger('click')
      expect(count.props('modelValue')).toBe(3)
      await format.get('button').trigger('click')
      await format.findAll('[role="option"]').find(option => option.text() === 'JPEG')!.trigger('click')
      await form.get('textarea').setValue('A mountain')
      await form.trigger('submit')
      await flushPromises()
      expect(submitBatchImageJob).toHaveBeenCalledWith('test-key-9', expect.objectContaining({
        model: 'gemini-test-image', response_mime_type: 'image/jpeg',
        items: [expect.objectContaining({ prompt: 'A mountain', output_count: 3 })],
      }), expect.any(String))
      await key.get('button').trigger('click')
      await key.findAll('[role="option"]').find(option => option.text() === 'batchImage.create.selectKeyPlaceholder')!.trigger('click')
      await flushPromises()
      expect(key.props('modelValue')).toBe(0)
      expect(model.props('modelValue')).toBe('')
      expect(model.props('disabled')).toBe(true)
    } finally { wrapper.unmount() }
  })
})
