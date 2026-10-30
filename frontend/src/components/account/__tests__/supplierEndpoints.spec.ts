import { describe, expect, it } from 'vitest'
import { getPlatformSpec } from '@/constants/platformCatalog'
import { CN_BASE_URL_PRESETS, GROK_BASE_URL_PRESETS } from '../credentialsBuilder'

const urls = (values: string[]) => [...new Set(values)].sort()
describe('official endpoint catalog matches administrator presets', () => {
  it('matches all Grok regions in both directions', () => {
    expect(urls(GROK_BASE_URL_PRESETS.map(p => p.url))).toEqual(urls(getPlatformSpec('grok')?.official_urls ?? []))
  })
  it.each(Object.entries(CN_BASE_URL_PRESETS))('matches %s modes and protocols in both directions', (platform, presets) => {
    const spec = getPlatformSpec(platform)!
    expect(urls(presets.map(p => p.url))).toEqual(urls(spec.official_urls ?? []))
    for (const mode of spec.multi_protocol!.modes) {
      for (const [protocol, official] of Object.entries(mode.official_base_urls ?? {})) {
        expect(urls(presets.filter(p => p.mode === mode.mode && p.protocol === protocol).map(p => p.url))).toEqual(urls(official))
      }
    }
  })
})
