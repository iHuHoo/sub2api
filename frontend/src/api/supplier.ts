import { apiClient } from './client'
import type { AccountUsageInfo, ClaudeModel, PaginatedResponse } from '@/types'

export interface SupplierAccount {
  id: number
  name: string
  notes: string | null
  platform: string
  type: string
  auth_mode?: 'service_account' | 'apikey'
  status: string
  read_only: boolean
  proxy_id: number | null
  expires_at: string | null
  last_used_at: string | null
  schedulable: boolean
  rate_limit_reset_at: string | null
  overload_until: string | null
  temp_unschedulable_until: string | null
  extra?: {
    model_rate_limits?: Record<string, { rate_limited_at: string; rate_limit_reset_at: string }>
  }
  created_at: string
  updated_at: string
}
export interface SupplierAccountCreate {
  name: string
  notes?: string | null
  platform: string
  type: string
  credentials: Record<string, string | number>
  proxy_id?: number | null
  expires_at?: number | null
}
export interface SupplierAccountUpdate {
  name?: string | null
  notes?: string | null
  credentials?: Record<string, string | number> | null
  proxy_id?: number | null
  expires_at?: number | null
}
export interface SupplierProxy {
  id: number
  name: string
  protocol: string
  status: string
  read_only: boolean
  expires_at?: string | null
  host?: string
  port?: number
  username?: string
  has_password?: boolean
  created_at?: string
  updated_at?: string
}
export interface SupplierProxyInput {
  name?: string | null
  protocol?: 'http' | 'https' | 'socks5' | 'socks5h' | null
  host?: string | null
  port?: number | null
  username?: string | null
  password?: string | null
  status?: 'active' | 'inactive' | null
}
export interface SupplierUsageTotals {
  requests: number
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
}
export interface SupplierUsage {
  timezone: 'UTC'
  summary: SupplierUsageTotals
  daily: (SupplierUsageTotals & { date: string })[]
}
export interface SupplierUsageQuery {
  start_date?: string
  end_date?: string
  account_id?: number
}

export const supplierAPI = {
  accounts: {
    async list(page = 1, pageSize = 20) {
      return (await apiClient.get<PaginatedResponse<SupplierAccount>>('/supplier/accounts', { params: { page, page_size: pageSize } })).data
    },
    async get(id: number) { return (await apiClient.get<SupplierAccount>(`/supplier/accounts/${id}`)).data },
    async getUsage(id: number) { return (await apiClient.get<AccountUsageInfo>(`/supplier/accounts/${id}/usage`)).data },
    async getModels(id: number) { return (await apiClient.get<ClaudeModel[]>(`/supplier/accounts/${id}/models`)).data },
    async create(input: SupplierAccountCreate) { return (await apiClient.post<SupplierAccount>('/supplier/accounts', input)).data },
    async update(id: number, input: SupplierAccountUpdate) { return (await apiClient.put<SupplierAccount>(`/supplier/accounts/${id}`, input)).data },
  },
  proxies: {
    async list(page = 1, pageSize = 20) {
      return (await apiClient.get<PaginatedResponse<SupplierProxy>>('/supplier/proxies', { params: { page, page_size: pageSize } })).data
    },
    async get(id: number) { return (await apiClient.get<SupplierProxy>(`/supplier/proxies/${id}`)).data },
    async create(input: SupplierProxyInput) { return (await apiClient.post<SupplierProxy>('/supplier/proxies', input)).data },
    async update(id: number, input: SupplierProxyInput) { return (await apiClient.put<SupplierProxy>(`/supplier/proxies/${id}`, input)).data },
    async delete(id: number) { return (await apiClient.delete<{ deleted: boolean }>(`/supplier/proxies/${id}`)).data },
    async test(id: number) { return (await apiClient.post<{ reachable: boolean; latency_ms: number }>(`/supplier/proxies/${id}/test`)).data }
  },
  async usage(params: SupplierUsageQuery = {}) {
    return (await apiClient.get<SupplierUsage>('/supplier/usage', { params })).data
  }
}

export function supplierErrorKey(error: unknown): string {
  const reason = (error as { reason?: string } | null)?.reason
  switch (reason) {
    case 'SUPPLIER_INVALID_INPUT': return 'supplier.invalidInput'
    case 'SUPPLIER_RESOURCE_UNAVAILABLE': return 'supplier.unavailable'
    case 'SUPPLIER_ACCOUNT_READ_ONLY': return 'supplier.readOnly'
    case 'SUPPLIER_PROXY_IN_USE': return 'supplier.proxyInUse'
    default: return 'supplier.failed'
  }
}
