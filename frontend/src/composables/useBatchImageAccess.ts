import { computed, ref } from 'vue'
import { keysAPI } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const loaded = ref(false)
const loading = ref(false)
const hasAllowedBatchImageKey = ref(false)
let pendingLoad: Promise<boolean> | null = null
const pageSize = 100
let requestGeneration = 0
let cachedIdentity: { token: string | null; userID?: number; role?: string } | null = null

function keyAllowsBatchImage(key: ApiKey): boolean {
  return (
    key.status === 'active' &&
    key.group?.platform === 'gemini' &&
    key.group?.allow_batch_image_generation === true
  )
}

async function loadBatchImageAccess(force = false): Promise<boolean> {
  const authStore = useAuthStore()
  if (!authStore.isAuthenticated || authStore.isSupplier) {
    requestGeneration++
    cachedIdentity = null
    pendingLoad = null
    loading.value = false
    loaded.value = true
    hasAllowedBatchImageKey.value = false
    return false
  }

  const identity = { token: authStore.token, userID: authStore.user?.id, role: authStore.user?.role }
  if (cachedIdentity?.token !== identity.token || cachedIdentity?.userID !== identity.userID || cachedIdentity?.role !== identity.role) {
    requestGeneration++
    cachedIdentity = identity
    pendingLoad = null
    loaded.value = false
    hasAllowedBatchImageKey.value = false
  }

  if (loaded.value && !force) {
    return hasAllowedBatchImageKey.value
  }

  if (pendingLoad && !force) {
    return pendingLoad
  }

  const generation = ++requestGeneration
  const isCurrentSession = () => generation === requestGeneration &&
    authStore.isAuthenticated && !authStore.isSupplier && authStore.token === identity.token &&
    authStore.user?.id === identity.userID && authStore.user?.role === identity.role
  loading.value = true
  pendingLoad = (async () => {
    let page = 1
    while (true) {
      if (!isCurrentSession()) return false
      const response = await keysAPI.list(page, pageSize, {
        status: 'active',
        sort_by: 'created_at',
        sort_order: 'desc'
      })

      if (!isCurrentSession()) return false

      if ((response.items || []).some(keyAllowsBatchImage)) {
        hasAllowedBatchImageKey.value = true
        loaded.value = true
        return true
      }

      if (page >= response.pages || (response.items || []).length === 0) {
        hasAllowedBatchImageKey.value = false
        loaded.value = true
        return false
      }

      page += 1
    }
  })()
    .catch(() => {
      if (isCurrentSession()) {
        hasAllowedBatchImageKey.value = false
        loaded.value = false
      }
      return false
    })
    .finally(() => {
      if (generation === requestGeneration) {
        loading.value = false
        pendingLoad = null
      }
    })

  return pendingLoad
}

export function useBatchImageAccess() {
  const authStore = useAuthStore()
  const canUseBatchImage = computed(() => authStore.isAuthenticated && !authStore.isSupplier &&
    authStore.token === cachedIdentity?.token && authStore.user?.id === cachedIdentity?.userID &&
    authStore.user?.role === cachedIdentity?.role && hasAllowedBatchImageKey.value)

  return {
    canUseBatchImage,
    batchImageAccessLoaded: computed(() => loaded.value),
    batchImageAccessLoading: computed(() => loading.value),
    refreshBatchImageAccess: loadBatchImageAccess,
  }
}
