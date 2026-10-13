import { beforeEach, expect, it, vi } from 'vitest'
import { watch } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { diagnosticsError, useProxyDiagnostics } from '../useProxyDiagnostics'
import complete from '@/components/proxy/__tests__/fixtures/complete.json'
const { checkDiagnostics } = vi.hoisted(() => ({ checkDiagnostics: vi.fn() }))
vi.mock('@/api/admin/proxies', () => ({ proxiesAPI: { checkDiagnostics } }))
beforeEach(() => vi.clearAllMocks())
const report = { report: complete, saved: true }
it.each([
  [{ status: 401 }, 'session_expired'],
  [{ status: 503 }, 'service_unavailable'],
  [{ status: 503, reason: 'service_auth_failed' }, 'service_auth_failed'],
  [{ status: 429 }, 'busy'],
  [{ status: 404 }, 'not_found'],
  [{ status: 409 }, 'proxy_changed'],
  [{ status: 400, reason: 'proxy_address_unsupported' }, 'proxy_address_unsupported'],
  [{ status: 503, reason: 'DIAGNOSTICS_DISABLED' }, 'disabled'],
  [{ reason: 'schema_unsupported' }, 'schema_unsupported'],
  [{ reason: 'constructor' }, 'service_unavailable'],
  [{ reason: 'toString' }, 'service_unavailable'],
  [{ reason: '__proto__' }, 'service_unavailable']
])('maps %j to a fixed error key', (error, reason) => {
  expect(diagnosticsError({ ...error, message: 'private upstream body' })).toBe(reason)
})
it('runs at most two requests and publishes each result before the batch finishes', async () => {
  const pending: Array<(v: unknown) => void> = []
  checkDiagnostics.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
  const d = useProxyDiagnostics()
  const run = d.run([1, 2, 3, 4], 'sample')
  expect(checkDiagnostics).toHaveBeenCalledTimes(2)
  pending[0](report); await flushPromises()
  expect(d.entries[1].result).toEqual(report)
  expect(d.entries[2].state).toBe('running')
  expect(checkDiagnostics).toHaveBeenCalledTimes(3)
  pending[1](report); pending[2](report); await flushPromises()
  pending[3](report); await run
  expect(Object.values(d.entries).every(e => e.state === 'done')).toBe(true)
})
it('on 429 stops new dispatch, keeps the other in-flight result, and never retries', async () => {
  let finish!: (v: unknown) => void
  checkDiagnostics.mockRejectedValueOnce({ status: 429, reason: 'busy' })
    .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const d = useProxyDiagnostics()
  const run = d.run([1, 2, 3, 4], 'quick')
  await flushPromises()
  expect(checkDiagnostics).toHaveBeenCalledTimes(2)
  finish(report); await run
  expect(d.entries[1].error).toBe('busy')
  expect(d.entries[2].result).toEqual(report)
  expect(d.entries[3].state).toBe('not_run')
  expect(d.busy.value).toBe(true)
})
it('cancel aborts in-flight calls, stops pending work, and suppresses late responses', async () => {
  const signals: AbortSignal[] = []
  const finishes: Array<(v: unknown) => void> = []
  checkDiagnostics.mockImplementation((_id, _mode, { signal }) => { signals.push(signal); return new Promise(resolve => finishes.push(resolve)) })
  const d = useProxyDiagnostics()
  const run = d.run([1, 2, 3], 'quick')
  d.cancel()
  expect(signals.every(signal => signal.aborted)).toBe(true)
  finishes.forEach(finish => finish(report)); await run
  expect(checkDiagnostics).toHaveBeenCalledTimes(2)
  expect(Object.values(d.entries).every(e => e.state === 'cancelled')).toBe(true)
  expect(Object.values(d.entries).every(e => !e.result)).toBe(true)
})

it('starts one replacement batch after cancelled requests finish without stale state', async () => {
  const finishes: Array<(v: unknown) => void> = []
  checkDiagnostics.mockImplementation(() => new Promise(resolve => finishes.push(resolve)))
  const d = useProxyDiagnostics()
  const old = d.run([1, 2, 3], 'quick')
  d.cancel()
  const next = d.run([1], 'sample')
  const duplicate = d.run([1], 'sample')
  finishes[0](report); finishes[1](report)
  await old; await flushPromises()
  expect(checkDiagnostics).toHaveBeenCalledTimes(3)
  expect(d.entries[1].state).toBe('running')
  expect(d.running.value).toBe(true)
  expect(d.entries[3].state).toBe('cancelled')
  finishes[2](report)
  await Promise.all([next, duplicate])
  expect(d.entries[1].result).toEqual(report)
  expect(d.running.value).toBe(false)
})
it('cancels a replacement waiting for the previous batch to settle', async () => {
  let finish!: (v: unknown) => void
  checkDiagnostics.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const d = useProxyDiagnostics()
  const old = d.run([1], 'quick')
  d.cancel()
  const next = d.run([2], 'sample')
  d.cancel()
  finish(report)
  await Promise.all([old, next])
  expect(checkDiagnostics).toHaveBeenCalledTimes(1)
  expect(d.entries[2]).toBeUndefined()
  expect(d.running.value).toBe(false)
})

it('accepts a new run in the previous run cleanup window', async () => {
  checkDiagnostics.mockResolvedValue(report)
  const d = useProxyDiagnostics()
  let next: Promise<void> | undefined
  const stop = watch(d.running, running => {
    if (!running && !next) next = d.run([2], 'sample')
  })
  await d.run([1], 'quick')
  await next
  stop()
  expect(d.entries[2]?.state).toBe('done')
})
