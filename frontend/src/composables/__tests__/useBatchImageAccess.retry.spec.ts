import { beforeEach, describe, expect, it, vi } from 'vitest'

const list = vi.hoisted(() => vi.fn())
const auth = vi.hoisted(() => ({ isAuthenticated: true, isSupplier: false, token: 'consumer-session', user: { id: 1, role: 'user' } }))
vi.mock('@/api/keys', () => ({ keysAPI: { list } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

beforeEach(() => { vi.resetModules(); list.mockReset(); auth.isSupplier = false; auth.isAuthenticated = true; auth.token = 'consumer-session'; auth.user = { id: 1, role: 'user' } })

describe('batch image access lookup', () => {
  it('retries a failed lookup when another consumer requests access', async () => {
    list.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({
      items: [{ status: 'active', group: { platform: 'gemini', allow_batch_image_generation: true } }], pages: 1,
    })
    const { useBatchImageAccess } = await import('../useBatchImageAccess')
    const access = useBatchImageAccess()
    expect(await access.refreshBatchImageAccess()).toBe(false)
    expect(access.batchImageAccessLoaded.value).toBe(false)
    expect(await useBatchImageAccess().refreshBatchImageAccess()).toBe(true)
    expect(list).toHaveBeenCalledTimes(2)
    expect(access.canUseBatchImage.value).toBe(true)
  })

  it('continues caching a successful lookup with no eligible keys', async () => {
    list.mockResolvedValue({ items: [], pages: 1 })
    const { useBatchImageAccess } = await import('../useBatchImageAccess')
    const access = useBatchImageAccess()
    expect(await access.refreshBatchImageAccess()).toBe(false)
    expect(access.batchImageAccessLoaded.value).toBe(true)
    expect(await access.refreshBatchImageAccess()).toBe(false)
    expect(list).toHaveBeenCalledTimes(1)
  })
})


it('never fetches consumer keys for a supplier, including a forced refresh', async () => {
  auth.isSupplier = true
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  const access = useBatchImageAccess()
  expect(await access.refreshBatchImageAccess(true)).toBe(false)
  expect(access.canUseBatchImage.value).toBe(false)
  expect(list).not.toHaveBeenCalled()
})


function deferred() {
  let resolve!: (value: unknown) => void
  const promise = new Promise<unknown>(finish => { resolve = finish })
  return { promise, resolve }
}

it('stops pagination before a second key fetch when a pending consumer response becomes supplier', async () => {
  const first = deferred()
  list.mockReturnValueOnce(first.promise).mockResolvedValue({ items: [], pages: 2 })
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  const access = useBatchImageAccess()
  const request = access.refreshBatchImageAccess()
  expect(list).toHaveBeenCalledTimes(1)
  auth.isSupplier = true
  auth.user = { id: 2, role: 'supplier' }
  auth.token = 'supplier-session'
  first.resolve({ items: [{ status: 'inactive' }], pages: 2 })
  expect(await request).toBe(false)
  expect(list).toHaveBeenCalledTimes(1)
  expect(access.batchImageAccessLoaded.value).toBe(false)
  expect(access.canUseBatchImage.value).toBe(false)
})

it('does not commit an eligible key from a stale supplier-session response', async () => {
  const first = deferred()
  list.mockReturnValueOnce(first.promise)
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  const access = useBatchImageAccess()
  const request = access.refreshBatchImageAccess()
  auth.isSupplier = true
  first.resolve({ items: [{ status: 'active', group: { platform: 'gemini', allow_batch_image_generation: true } }], pages: 1 })
  expect(await request).toBe(false)
  expect(access.batchImageAccessLoaded.value).toBe(false)
})

it('does not reuse another consumer session cache or commit an older session lookup', async () => {
  const first = deferred()
  list.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ items: [], pages: 1 })
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  const access = useBatchImageAccess()
  const oldRequest = access.refreshBatchImageAccess()
  auth.token = 'new-consumer-session'
  auth.user = { id: 2, role: 'user' }
  const newRequest = access.refreshBatchImageAccess()
  expect(list).toHaveBeenCalledTimes(2)
  expect(await newRequest).toBe(false)
  first.resolve({ items: [{ status: 'active', group: { platform: 'gemini', allow_batch_image_generation: true } }], pages: 1 })
  expect(await oldRequest).toBe(false)
  expect(access.canUseBatchImage.value).toBe(false)
  expect(access.batchImageAccessLoaded.value).toBe(true)
})

it('keeps the latest forced lookup result when older responses finish afterward', async () => {
  const first = deferred()
  const second = deferred()
  list.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  const access = useBatchImageAccess()
  const oldRequest = access.refreshBatchImageAccess()
  const currentRequest = access.refreshBatchImageAccess(true)
  second.resolve({ items: [], pages: 1 })
  expect(await currentRequest).toBe(false)
  first.resolve({ items: [{ status: 'active', group: { platform: 'gemini', allow_batch_image_generation: true } }], pages: 1 })
  expect(await oldRequest).toBe(false)
  expect(access.canUseBatchImage.value).toBe(false)
  expect(access.batchImageAccessLoaded.value).toBe(true)
})
