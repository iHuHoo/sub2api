export type ProxyDiagnosticsMode = 'quick' | 'sample'
export type ProxyDiagnosticsStatus = 'pass' | 'warn' | 'fail' | 'challenge' | 'unknown' | 'skipped'
export interface ProxyDiagnosticsExit {
  ip: string
  source: string
  observed_at: string
  country_code?: string
  country?: string
  region?: string
  city?: string
  asn?: string
  organization?: string
}
export interface ProxyDiagnosticsSample {
  status: ProxyDiagnosticsStatus
  reason_code: string
  ip?: string
  source?: string
  latency_ms?: number
}
export interface ProxyDiagnosticsItem {
  id: string
  plugin_id: string
  plugin_version: string
  rule_id: string
  rule_version: string
  category: string
  target: string
  status: ProxyDiagnosticsStatus
  reason_code: string
  message: string
  started_at: string
  duration_ms?: number
  http_status?: number
  cf_ray?: string
  evidence?: Record<string, string | number | boolean>
  exit?: ProxyDiagnosticsExit
  samples?: ProxyDiagnosticsSample[]
}
export interface ProxyDiagnosticsReport {
  schema_version: number
  ruleset_version: string
  request_id: string
  checked_at: string
  mode: ProxyDiagnosticsMode
  execution: 'completed' | 'partial'
  connectivity: 'unusable' | 'degraded' | 'reachable' | 'unknown'
  exit: ProxyDiagnosticsExit | null
  stability: {
    status: ProxyDiagnosticsStatus
    reason_code: string
    attempted: number
    succeeded: number
    distinct_ips: number
    latency_min_ms?: number
    latency_median_ms?: number
    latency_max_ms?: number
    samples: ProxyDiagnosticsSample[]
  }
  items: ProxyDiagnosticsItem[]
  summary: string
  completion: { planned: number; executed: number; conclusive: number; unknown: number; skipped: number }
}
export interface ProxyDiagnosticsResponse { report: ProxyDiagnosticsReport; saved: boolean }
export interface ProxyDiagnosticsCapabilities { enabled: boolean; schema_version: number; modes: ProxyDiagnosticsMode[] }
