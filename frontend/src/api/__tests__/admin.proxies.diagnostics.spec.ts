import { beforeEach, expect, it, vi } from 'vitest'
import { proxiesAPI } from '../admin/proxies'
import complete from '@/components/proxy/__tests__/fixtures/complete.json'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get, post } }))
beforeEach(() => vi.clearAllMocks())

it('overrides the 30s client timeout and sends only mode with caller cancellation', async () => {
  const signal = new AbortController().signal
  post.mockResolvedValue({ data: { report: complete, saved: true } })
  await proxiesAPI.checkDiagnostics(7, 'sample', { signal })
  expect(post).toHaveBeenCalledWith('/admin/proxies/7/diagnostics', { mode: 'sample' }, { timeout: 70000, signal })
})
it('reads capabilities and the saved report separately', async () => {
  get.mockResolvedValueOnce({ data: { enabled: false, schema_version: 1, modes: ['quick', 'sample'] } })
  expect((await proxiesAPI.diagnosticsCapabilities()).enabled).toBe(false)
  get.mockResolvedValueOnce({ data: { report: complete, saved: true } })
  await proxiesAPI.getDiagnostics(7)
  expect(get.mock.calls.map(args => args[0])).toEqual(['/admin/proxies/diagnostics-capabilities', '/admin/proxies/7/diagnostics'])
})
it('rejects an unsupported report major version explicitly', async () => {
  post.mockResolvedValue({ data: { report: { ...complete, schema_version: 2 }, saved: true } })
  await expect(proxiesAPI.checkDiagnostics(7, 'quick')).rejects.toMatchObject({ reason: 'schema_unsupported' })
})
it.each([undefined, null, {}, { report: null }, { report: {} }, { report: { schema_version: '1' } }])('classifies malformed response %j as service failure', async data => {
  post.mockResolvedValue({ data })
  get.mockResolvedValue({ data })
  await expect(proxiesAPI.checkDiagnostics(7, 'quick')).rejects.toMatchObject({ reason: 'service_unavailable' })
  await expect(proxiesAPI.getDiagnostics(7)).rejects.toMatchObject({ reason: 'service_unavailable' })
})
