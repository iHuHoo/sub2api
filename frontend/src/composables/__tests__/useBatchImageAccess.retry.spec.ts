import { beforeEach, describe, expect, it, vi } from 'vitest'

const list = vi.hoisted(() => vi.fn())
const auth = vi.hoisted(() => ({ isAuthenticated: true, isSupplier: false }))
vi.mock('@/api/keys', () => ({ keysAPI: { list } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

beforeEach(() => { vi.resetModules(); list.mockReset(); auth.isSupplier = false })

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
