import { expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ProxyDiagnosticsReport from '../ProxyDiagnosticsReport.vue'
import complete from './fixtures/complete.json'
import partial from './fixtures/partial.json'
import unusable from './fixtures/unusable.json'
import type { ProxyDiagnosticsReport as Report } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string) => key,
  te: (key: string) => key.split('.').at(-1)! in { expected_status: 'Expected HTTP status' },
  tm: () => ({ expected_status: 'Expected HTTP status' })
}) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
const render = (report: unknown, saved = true) => mount(ProxyDiagnosticsReport, { props: { report: report as Report, saved } })
it.each([complete, partial, unusable])('renders contract report $request_id without old quality scores', report => {
  const wrapper = render(report)
  expect(wrapper.text()).toContain(report.request_id)
  expect(wrapper.findAll('[data-test="diagnostic-item"]')).toHaveLength(report.items.length)
  expect(wrapper.find('[data-testid="quality-score"]').exists()).toBe(false)
  expect(wrapper.text()).toContain(report.ruleset_version)
})
it('renders new plugins, unknown reasons, challenge and missing geography as text', () => {
  const report = structuredClone(complete)
  report.items[0] = { ...report.items[0], id: 'custom.check', plugin_id: 'future.plugin', reason_code: 'new_reason', status: 'challenge', message: '<script>bad()</script>' }
  const wrapper = render({ ...report, exit: null }, false)
  expect(wrapper.text()).toContain('custom.check')
  expect(wrapper.text()).toContain('new_reason')
  expect(wrapper.text()).toContain('<script>bad()</script>')
  expect(wrapper.find('script').exists()).toBe(false)
  expect(wrapper.get('[data-test="diagnostic-item"]').attributes('data-status')).toBe('challenge')
  expect(wrapper.text()).toContain('admin.proxies.diagnostics.notSaved')
  expect(wrapper.text()).toContain('admin.proxies.diagnostics.noExit')
})
it('renders backend-filtered plugin scalars as inert text and ignores structured evidence', () => {
  const wrapper = render({ ...partial, items: [{ ...partial.items[0], evidence: {
    future_metric: 42, future_flag: false, future_text: '<img src=x onerror=alert(1)>',
    nested: { secret: 'nested-secret' }, list: ['array-secret'], empty: null
  } }] })
  expect(wrapper.text()).toContain('future_metric')
  expect(wrapper.text()).toContain('42')
  expect(wrapper.text()).toContain('false')
  expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>')
  expect(wrapper.find('img').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('nested-secret')
  expect(wrapper.text()).not.toContain('array-secret')
  expect(wrapper.text()).not.toContain('empty')
})
it('never renders an incompatible schema as a valid report', () => {
  const wrapper = render({ ...complete, schema_version: 2 })
  expect(wrapper.text()).toContain('admin.proxies.diagnostics.errors.schema_unsupported')
  expect(wrapper.find('[data-test="diagnostic-item"]').exists()).toBe(false)
})
it.each(['constructor', 'toString', '__proto__'])('treats prototype name %s as an unknown reason', reason => {
  const wrapper = render({ ...complete, items: [{ ...complete.items[0], reason_code: reason }] })
  expect(wrapper.text()).toContain(`admin.proxies.diagnostics.unknownReason: ${reason}`)
  expect(wrapper.text()).not.toContain(`admin.proxies.diagnostics.reasons.${reason}`)
})
it('renders stability samples once while retaining samples from new plugins', () => {
  const samples = [{ status: 'pass', reason_code: 'exit_observed', ip: '203.0.113.7', latency_ms: 25 }]
  const wrapper = render({ ...complete, mode: 'sample',
    stability: { ...complete.stability, samples },
    items: [
      { ...complete.items[0], id: 'stability', category: 'stability', samples },
      { ...complete.items[0], id: 'future', category: 'connectivity', samples: [{ ...samples[0], ip: '203.0.113.8' }] }
    ]
  })
  expect(wrapper.text().match(/203\.0\.113\.7/g)).toHaveLength(1)
  expect(wrapper.text().match(/203\.0\.113\.8/g)).toHaveLength(1)
  expect(wrapper.findAll('[data-test="diagnostic-item"]')).toHaveLength(2)
})
