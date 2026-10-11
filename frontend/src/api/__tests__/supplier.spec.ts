import { describe, expect, it, vi } from 'vitest'
import { supplierAPI, supplierErrorKey } from '../supplier'
const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))
describe('supplier API transport', () => {
  it('uses the restricted paginated resource endpoint', async () => {
    const response = { items: [{ id: 3 }], total: 1, page: 2, page_size: 20, pages: 2 }
    client.get.mockResolvedValue({ data: response })
    expect(await supplierAPI.accounts.list(2)).toEqual(response)
    expect(client.get).toHaveBeenCalledWith('/supplier/accounts', { params: { page: 2, page_size: 20 } })
  })
  it('sends sparse account edits and a separate supplier pause mutation', async () => {
    client.put.mockResolvedValue({ data: { id: 3 } })
    client.post.mockResolvedValue({ data: { id: 3 } })
    await supplierAPI.accounts.update(3, { credentials: { api_key: 'synthetic' } })
    expect(client.put).toHaveBeenCalledWith('/supplier/accounts/3', { credentials: { api_key: 'synthetic' } })
    await supplierAPI.accounts.pause(3, false)
    expect(client.post).toHaveBeenCalledWith('/supplier/accounts/3/pause', { paused: false })
    expect('delete' in supplierAPI.accounts).toBe(false)
  })
  it('never accepts a target URL or body for TCP testing', async () => {
    client.post.mockResolvedValue({ data: { reachable: false, latency_ms: 5000 } })
    expect(await supplierAPI.proxies.test(7)).toEqual({ reachable: false, latency_ms: 5000 })
    expect(client.post).toHaveBeenCalledWith('/supplier/proxies/7/test')
  })
  it.each([
    ['SUPPLIER_INVALID_INPUT', 'supplier.invalidInput'], ['SUPPLIER_RESOURCE_UNAVAILABLE', 'supplier.unavailable'],
    ['SUPPLIER_ACCOUNT_READ_ONLY', 'supplier.readOnly'], ['SUPPLIER_PROXY_IN_USE', 'supplier.proxyInUse'],
    ['internal error with credentials', 'supplier.failed'], [undefined, 'supplier.failed']
  ])('only displays safe error text for %s', (reason, key) => {
    expect(supplierErrorKey({ reason, message: 'raw internal error' })).toBe(key)
  })
})
