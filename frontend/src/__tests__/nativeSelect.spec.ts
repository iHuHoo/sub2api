import { describe, expect, it } from 'vitest'

const sources = import.meta.glob('../**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('native select regression guard', () => {
  it('uses common/Select.vue for every single selection', () => {
    const violations: string[] = []
    for (const [file, source] of Object.entries(sources)) {
      const selects = source.match(/<select\b[^>]*>/g) ?? []
      const allowed = file === '../components/admin/settings/AdminAccessKeysSettings.vue'
      if (allowed && selects.length <= 2 && selects.every(tag => /\bmultiple\b/.test(tag))) continue
      if (selects.length) violations.push(`${file}: ${selects.length} native select(s); use components/common/Select.vue`)
    }
    expect(violations, violations.join('\n')).toEqual([])
  })
})
