import { apiClient } from './client'

export interface ClashMetadata {
 exists: boolean
 active: boolean
 version: number
 expires_at: string | null
 revoked_at: string | null
 exportable_count: number
 skipped_count: number
}
export interface ClashGrant { metadata: ClashMetadata; subscription_url: string }
export function clashSubscriptionAPI(role: 'admin' | 'supplier') {
 const base = `/${role}/clash-subscription`
 return {
  metadata: async () => (await apiClient.get<ClashMetadata>(base)).data,
  create: async (duration_days: number) => (await apiClient.post<ClashGrant>(base, { duration_days })).data,
  reset: async (expected_version: number, duration_days: number) => (await apiClient.post<ClashGrant>(`${base}/reset`, { expected_version, duration_days })).data,
  revoke: async (expected_version: number) => { await apiClient.delete(base, { data: { expected_version } }) }
 }
}
