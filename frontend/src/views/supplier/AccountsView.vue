<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import { supplierAPI, supplierErrorKey, type SupplierAccount, type SupplierAccountUpdate, type SupplierProxy } from '@/api/supplier'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const accounts = ref<SupplierAccount[]>([])
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const saving = ref(false)
const pendingPause = ref<number | null>(null)
const show = ref(false)
const editing = ref<SupplierAccount | null>(null)
const initialExpiry = ref('')
const proxies = ref<SupplierProxy[]>([])
const proxyPage = ref(1)
const proxyTotal = ref(0)
const proxyLoading = ref(false)
const form = reactive({ name: '', notes: '', platform: 'anthropic', type: 'apikey', proxy_id: '', expires_at: '', clear_expiry: false })
const credentials = reactive<Record<string, string>>({})
const clearCredentials = reactive<Record<string, boolean>>({})

const platformTypes: Record<string, string[]> = {
  anthropic: ['apikey', 'upstream', 'oauth', 'setup-token', 'bedrock', 'service_account'],
  openai: ['apikey', 'upstream', 'oauth'], gemini: ['apikey', 'oauth', 'service_account'],
  antigravity: ['oauth'], grok: ['apikey', 'oauth'],
  kimi: ['apikey'], zhipu: ['apikey'], deepseek: ['apikey'], minimax: ['apikey'],
  opencode_go: ['apikey'], typesafe: ['apikey'], command_code: ['apikey'], cline: ['apikey']
}
const officialURLs: Record<string, string[]> = {
  anthropic: ['https://api.anthropic.com'], openai: ['https://api.openai.com/v1'],
  gemini: ['https://generativelanguage.googleapis.com'], grok: ['https://api.x.ai/v1'],
  kimi: ['https://api.moonshot.cn/v1', 'https://api.kimi.com/coding/v1'],
  zhipu: ['https://open.bigmodel.cn/api/paas/v4', 'https://open.bigmodel.cn/api/coding/paas/v4'],
  deepseek: ['https://api.deepseek.com'], minimax: ['https://api.minimaxi.com/v1'],
  opencode_go: ['https://opencode.ai/zen/go/v1', 'https://opencode.ai/zen/v1'],
  typesafe: ['https://api.typesafe.ai'], command_code: ['https://api.commandcode.ai/provider/v1'], cline: ['https://api.cline.bot/api/v1']
}
const supported = computed(() => platformTypes[form.platform]?.includes(form.type) ?? false)
const fields = computed(() => {
  if (!supported.value) return []
  let required: string[] = []
  let optional: string[] = []
  switch (form.type) {
    case 'apikey': required = ['api_key']; optional = ['base_url']; break
    case 'upstream': required = ['api_key', 'base_url']; break
    case 'oauth':
      required = ['access_token']
      optional = ['refresh_token', 'id_token', 'expires_at', 'token_type', 'scope', 'email']
      if (form.platform === 'openai') optional.push('chatgpt_account_id', 'chatgpt_user_id', 'organization_id')
      if (form.platform === 'anthropic') optional.push('claude_user_id', 'anthropic_user_id')
      if (['gemini', 'antigravity'].includes(form.platform)) optional.push('project_id')
      break
    case 'setup-token': required = ['access_token']; optional = ['expires_at']; break
    case 'service_account': required = ['service_account_json']; optional = ['project_id', 'location']; break
    case 'bedrock':
      required = ['aws_region', 'auth_mode']
      if (credentials.auth_mode === 'apikey') required.push('api_key')
      if (credentials.auth_mode === 'sigv4' || !credentials.auth_mode) {
        required.push('aws_access_key_id', 'aws_secret_access_key')
        optional = ['aws_session_token']
      }
      break
  }
  return [...required.map(key => ({ key, required: true })), ...optional.map(key => ({ key, required: false }))]
})
const columns = computed(() => [
  { key: 'name', label: t('supplier.name') }, { key: 'platform', label: t('supplier.platform') },
  { key: 'status', label: t('supplier.adminLifecycle') }, { key: 'supplier_paused', label: t('supplier.supply') },
  { key: 'expires_at', label: t('supplier.expiry') }, { key: 'actions', label: t('common.actions') }
])
function resetCredentials() {
  Object.keys(credentials).forEach(key => delete credentials[key])
  Object.keys(clearCredentials).forEach(key => delete clearCredentials[key])
  if (!editing.value && form.type === 'bedrock') credentials.auth_mode = 'sigv4'
}
watch(() => form.platform, () => {
  if (!editing.value) form.type = platformTypes[form.platform]?.[0] ?? 'apikey'
  resetCredentials()
})
watch(() => form.type, resetCredentials)

async function load() {
  loading.value = true
  try {
    const data = await supplierAPI.accounts.list(page.value)
    accounts.value = data.items
    total.value = data.total
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { loading.value = false }
}
async function loadProxies() {
  proxyLoading.value = true
  try {
    const data = await supplierAPI.proxies.list(proxyPage.value)
    proxies.value = data.items
    proxyTotal.value = data.total
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { proxyLoading.value = false }
}
function open(account: SupplierAccount | null = null) {
  if (account?.read_only) return
  editing.value = account
  initialExpiry.value = account?.expires_at ? new Date(account.expires_at).toISOString().slice(0, 16) : ''
  Object.assign(form, {
    name: account?.name ?? '', notes: account?.notes ?? '', platform: account?.platform ?? 'anthropic', type: account?.type ?? 'apikey',
    proxy_id: '', expires_at: initialExpiry.value, clear_expiry: false
  })
  resetCredentials()
  proxyPage.value = 1
  show.value = true
  void loadProxies()
}
function canClear(field: { key: string; required: boolean }) {
  return !field.required && !['base_url', 'project_id', 'location', 'expires_at'].includes(field.key)
}
function isSecret(key: string) {
  return ['api_key', 'access_token', 'refresh_token', 'id_token', 'aws_access_key_id', 'aws_secret_access_key', 'aws_session_token'].includes(key)
}
async function save() {
  if (saving.value || editing.value?.read_only) return
  if (!form.name.trim() || new TextEncoder().encode(form.name).length > 100 || new TextEncoder().encode(form.notes).length > 2000) {
    appStore.showError(t('supplier.invalidInput'))
    return
  }
  const changes: SupplierAccountUpdate = {}
  const imported: Record<string, string | number> = {}
  for (const field of fields.value) {
    if (clearCredentials[field.key] && canClear(field)) imported[field.key] = ''
    else if (credentials[field.key]) imported[field.key] = credentials[field.key]
    else if (!editing.value && field.required) {
      appStore.showError(t('supplier.requiredCredential', { field: field.key }))
      return
    }
  }
  if (Object.keys(imported).length) changes.credentials = imported
  if (!editing.value || form.name !== editing.value.name) changes.name = form.name
  if (!editing.value || form.notes !== (editing.value.notes ?? '')) changes.notes = form.notes
  if (form.proxy_id !== '') changes.proxy_id = Number(form.proxy_id)
  if (form.clear_expiry) changes.expires_at = 0
  else if (form.expires_at && form.expires_at !== initialExpiry.value) {
    const seconds = Date.parse(form.expires_at + ':00Z') / 1000
    if (!Number.isInteger(seconds) || seconds < 0 || seconds > 253402300799) {
      appStore.showError(t('supplier.invalidInput'))
      return
    }
    changes.expires_at = seconds
  }
  saving.value = true
  try {
    if (editing.value) await supplierAPI.accounts.update(editing.value.id, changes)
    else await supplierAPI.accounts.create({ ...changes, name: form.name, platform: form.platform, type: form.type, credentials: imported })
    appStore.showSuccess(t('supplier.saved'))
    show.value = false
    resetCredentials()
    await load()
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { saving.value = false }
}
async function pause(account: SupplierAccount) {
  if (account.read_only || pendingPause.value !== null) return
  pendingPause.value = account.id
  try {
    await supplierAPI.accounts.pause(account.id, !account.supplier_paused)
    await load()
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { pendingPause.value = null }
}
function close() {
  if (saving.value) return
  show.value = false
  resetCredentials()
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('supplier.accounts') }}</h2>
        <button class="btn btn-primary" @click="open()">{{ t('supplier.addAccount') }}</button>
      </div>
      <p class="card p-4 text-sm text-gray-600 dark:text-gray-300">{{ t('supplier.assignmentNotice') }}</p>
      <div class="card overflow-hidden">
        <DataTable :columns="columns" :data="accounts" :loading="loading">
          <template #cell-name="{ row }">
            <div class="font-medium">{{ row.name }} <span class="text-xs text-gray-500">#{{ row.id }}</span></div>
            <p v-if="row.notes" class="max-w-xs whitespace-pre-wrap break-words text-xs text-gray-500">{{ row.notes }}</p>
            <span v-if="row.read_only" class="badge badge-gray">{{ t('supplier.readOnly') }}</span>
          </template>
          <template #cell-platform="{ row }">{{ row.platform }} / {{ row.type }}</template>
          <template #cell-status="{ value }">{{ value === 'active' ? t('common.active') : value === 'inactive' ? t('supplier.inactive') : value }}</template>
          <template #cell-supplier_paused="{ value }">{{ value ? t('supplier.paused') : t('supplier.unpaused') }}</template>
          <template #cell-expires_at="{ value }">{{ value || '—' }}</template>
          <template #cell-actions="{ row }">
            <div class="flex flex-wrap gap-2">
              <button class="btn btn-secondary btn-sm" :disabled="row.read_only" @click="open(row)">{{ t('common.edit') }}</button>
              <button class="btn btn-secondary btn-sm" :disabled="row.read_only || pendingPause !== null" @click="pause(row)">{{ row.supplier_paused ? t('supplier.resume') : t('supplier.pause') }}</button>
            </div>
          </template>
        </DataTable>
        <Pagination v-if="total" :total="total" :page="page" :page-size="20" :show-page-size-selector="false" @update:page="page = $event; load()" />
      </div>
    </div>
    <BaseDialog :show="show" :title="editing ? t('supplier.editAccount') : t('supplier.addAccount')" width="normal" @close="close">
      <form id="supplier-account-form" class="space-y-4" @submit.prevent="save">
        <label class="block"><span class="input-label">{{ t('supplier.name') }}</span><input v-model="form.name" name="name" class="input" required maxlength="100" /></label>
        <label class="block"><span class="input-label">{{ t('supplier.notes') }}</span><textarea v-model="form.notes" name="notes" class="input" rows="2" maxlength="2000" /></label>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block"><span class="input-label">{{ t('supplier.platform') }}</span><select v-model="form.platform" name="platform" class="input" :disabled="!!editing"><option v-if="editing && !platformTypes[form.platform]" :value="form.platform">{{ form.platform }}</option><option v-for="(_, platform) in platformTypes" :key="platform" :value="platform">{{ platform }}</option></select></label>
          <label class="block"><span class="input-label">{{ t('supplier.accountType') }}</span><select v-model="form.type" name="type" class="input" :disabled="!!editing"><option v-if="editing && !supported" :value="form.type">{{ form.type }}</option><option v-for="type in platformTypes[form.platform] || []" :key="type" :value="type">{{ type }}</option></select></label>
        </div>
        <p class="input-hint">{{ t('supplier.importNotice') }}</p>
        <p v-if="editing" class="input-hint">{{ t('supplier.retainCredential') }}</p>
        <p v-if="!supported" class="input-hint">{{ t('supplier.unsupportedCredential') }}</p>
        <div v-for="field in fields" :key="field.key" class="space-y-1">
          <label :for="'credential-' + field.key" class="input-label">{{ t('supplier.credential.' + field.key) }} <span class="font-mono text-xs">({{ field.key }})</span><span v-if="field.required && !editing"> *</span></label>
          <select v-if="field.key === 'base_url'" :id="'credential-' + field.key" v-model="credentials[field.key]" :name="field.key" class="input" :required="field.required && !editing">
            <option value="">{{ editing ? t('supplier.retainEndpoint') : t('supplier.defaultEndpoint') }}</option><option v-for="url in officialURLs[form.platform]" :key="url" :value="url">{{ url }}</option>
          </select>
          <select v-else-if="field.key === 'auth_mode'" :id="'credential-' + field.key" v-model="credentials[field.key]" name="auth_mode" class="input" :required="!editing"><option v-if="editing" value="">{{ t('supplier.retain') }}</option><option value="sigv4">AWS SigV4</option><option value="apikey">API Key</option></select>
          <textarea v-else-if="field.key === 'service_account_json'" :id="'credential-' + field.key" v-model="credentials[field.key]" :name="field.key" class="input font-mono" rows="5" :required="!editing" maxlength="32768" :placeholder="t('supplier.serviceAccountHint')" />
          <input v-else :id="'credential-' + field.key" v-model="credentials[field.key]" :name="field.key" :type="isSecret(field.key) ? 'password' : 'text'" class="input" :required="field.required && !editing" :maxlength="field.key === 'expires_at' ? 64 : 16384" autocomplete="off" :disabled="clearCredentials[field.key]" :placeholder="field.key === 'expires_at' ? t('supplier.tokenExpiryHint') : editing ? t('supplier.retainCredential') : ''" />
          <label v-if="editing && canClear(field)" class="flex items-center gap-2 text-sm"><input v-model="clearCredentials[field.key]" type="checkbox" />{{ t('supplier.clearCredential', { field: field.key }) }}</label>
        </div>
        <p v-if="form.type === 'service_account'" class="input-hint">{{ t('supplier.serviceAccountNotice') }}</p>
        <p class="input-hint">{{ t('supplier.endpointPolicy') }}</p>
        <label class="block"><span class="input-label">{{ t('supplier.proxy') }}</span><select v-model="form.proxy_id" name="proxy_id" class="input" :disabled="proxyLoading"><option value="">{{ editing ? t('supplier.retainProxy') : t('supplier.noProxy') }}</option><option v-if="editing" value="0">{{ t('supplier.detachProxy') }}</option><option v-if="editing?.proxy_id && !proxies.some(p => p.id === editing?.proxy_id)" :value="editing.proxy_id">#{{ editing.proxy_id }}</option><option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">{{ proxy.name }} · #{{ proxy.id }} · {{ proxy.status }}</option></select></label>
        <Pagination v-if="proxyTotal > 20" :total="proxyTotal" :page="proxyPage" :page-size="20" :show-page-size-selector="false" @update:page="proxyPage = $event; loadProxies()" />
        <label class="block"><span class="input-label">{{ t('supplier.expiryUTC') }}</span><input v-model="form.expires_at" name="expires_at" type="datetime-local" class="input" :disabled="form.clear_expiry" min="1970-01-01T00:00" max="9999-12-31T23:59" /></label>
        <label v-if="editing" class="flex items-center gap-2 text-sm"><input v-model="form.clear_expiry" name="clear_expiry" type="checkbox" />{{ t('supplier.clearExpiry') }}</label>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button form="supplier-account-form" type="submit" class="btn btn-primary" :disabled="saving">{{ t('common.save') }}</button></template>
    </BaseDialog>
  </AppLayout>
</template>
