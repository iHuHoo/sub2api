import { reactive, ref } from 'vue'
import { proxiesAPI } from '@/api/admin/proxies'
import type { ProxyDiagnosticsMode, ProxyDiagnosticsResponse } from '@/types'

export type DiagnosticsError = 'service_unavailable' | 'service_auth_failed' | 'session_expired' | 'busy' | 'not_found' | 'proxy_changed' | 'proxy_address_unsupported' | 'request_invalid' | 'disabled' | 'schema_unsupported' | 'cancelled'
export function diagnosticsError(error: unknown): DiagnosticsError {
  const e = error as { status?: number; reason?: string; code?: string }
  if (e?.code === 'ERR_CANCELED') return 'cancelled'
  if (e?.status === 401) return 'session_expired'
  const reasons: Record<string, DiagnosticsError> = {
    service_auth_failed: 'service_auth_failed', busy: 'busy', REPORT_NOT_FOUND: 'not_found',
    PROXY_NOT_FOUND: 'not_found', PROXY_CHANGED: 'proxy_changed', DIAGNOSTICS_DISABLED: 'disabled',
    proxy_address_unsupported: 'proxy_address_unsupported', request_invalid: 'request_invalid',
    schema_unsupported: 'schema_unsupported'
  }
  const reason = e?.reason ?? ''
  return (Object.prototype.hasOwnProperty.call(reasons, reason) ? reasons[reason] : undefined) ?? ({ 429: 'busy', 404: 'not_found', 409: 'proxy_changed' } as Record<number, DiagnosticsError>)[e?.status ?? 0] ?? 'service_unavailable'
}
export interface DiagnosticsEntry {
  state: 'queued' | 'running' | 'done' | 'failed' | 'cancelled' | 'not_run'
  result?: ProxyDiagnosticsResponse
  error?: DiagnosticsError
}
export function useProxyDiagnostics() {
  const entries = reactive<Record<number, DiagnosticsEntry>>({})
  const running = ref(false)
  const busy = ref(false)
  let controller: AbortController | undefined
  function cancel() { controller?.abort() }
  async function run(ids: number[], mode: ProxyDiagnosticsMode) {
    if (running.value) return
    const unique = [...new Set(ids)]
    controller = new AbortController()
    const signal = controller.signal
    running.value = true
    busy.value = false
    for (const id of unique) entries[id] = { state: 'queued' }
    let cursor = 0
    async function worker() {
      while (!signal.aborted && !busy.value && cursor < unique.length) {
        const id = unique[cursor++]
        entries[id] = { state: 'running' }
        try {
          const result = await proxiesAPI.checkDiagnostics(id, mode, { signal })
          if (!signal.aborted) entries[id] = { state: 'done', result }
        } catch (error) {
          if (!signal.aborted) {
            const reason = diagnosticsError(error)
            entries[id] = { state: reason === 'cancelled' ? 'cancelled' : 'failed', error: reason }
            if (reason === 'busy') busy.value = true
          }
        } finally {
          if (signal.aborted) entries[id] = { state: 'cancelled' }
        }
      }
    }
    try { await Promise.all([worker(), worker()]) }
    finally {
      for (const id of unique) if (entries[id].state === 'queued') entries[id] = { state: signal.aborted ? 'cancelled' : 'not_run' }
      running.value = false
      controller = undefined
    }
  }
  return { entries, running, busy, run, cancel }
}
