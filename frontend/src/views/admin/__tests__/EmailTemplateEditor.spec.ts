import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import EmailTemplateEditor from '../settings/EmailTemplateEditor.vue'

vi.mock('@/api', () => ({
  adminAPI: { settings: { getEmailTemplates: vi.fn().mockResolvedValue({ events: [], locales: [] }) } },
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
}))

describe('EmailTemplateEditor accessible names', () => {
  it('names the event and locale triggers after their visible labels', async () => {
    const wrapper = mount(EmailTemplateEditor)
    await flushPromises()
    expect(wrapper.get('button#email-template-event').attributes('aria-label')).toBe('admin.settings.emailTemplates.event')
    expect(wrapper.get('button#email-template-locale').attributes('aria-label')).toBe('admin.settings.emailTemplates.locale')
    wrapper.unmount()
  })
})
