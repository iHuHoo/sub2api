<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import CreateAccountModal from '@/components/account/CreateAccountModal.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import AccountTestModal from '@/components/admin/account/AccountTestModal.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import Select from '@/components/common/Select.vue'
import { supplierAPI, supplierErrorKey, type SupplierAccount, type SupplierAccountUpdate, type SupplierProxy } from '@/api/supplier'
import { useAppStore } from '@/stores/app'
import { getProviderProfile, officialBaseURLs } from '@/constants/platformCatalog'
import { providerModeLabel } from '@/components/account/credentialsBuilder'
import type { Account } from '@/types'
import { formatDateTime, formatRelativeTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const accounts = ref<SupplierAccount[]>([])
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const saving = ref(false)
const show = ref(false)
const showCreate = ref(false)
const createLoading = ref(false)
const createProxies = ref<SupplierProxy[]>([])
const editing = ref<SupplierAccount | null>(null)
const testing = ref<SupplierAccount | null>(null)
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
const supported = computed(() => platformTypes[form.platform]?.includes(form.type) ?? false)
const platformOptions = computed(() => [
  ...(!platformTypes[form.platform] ? [{ value: form.platform, label: form.platform }] : []),
  ...Object.keys(platformTypes).map(platform => ({ value: platform, label: platform }))
])
const typeOptions = computed(() => [
  ...(!supported.value ? [{ value: form.type, label: form.type }] : []),
  ...(platformTypes[form.platform] || []).map(type => ({ value: type, label: type }))
])
const baseURLOptions = computed(() => [
  { value: '', label: t('supplier.retainEndpoint') },
  ...officialBaseURLs(form.platform, credentials.account_mode, credentials.api_protocol === 'adaptive' ? 'chat_completions' : credentials.api_protocol).map(url => ({ value: url, label: url }))
])
const vertexAPIKeyEdit = computed(() => form.platform === 'gemini' && form.type === 'service_account' && editing.value?.auth_mode === 'apikey')
const providerProfile = computed(() => form.type === 'apikey' ? getProviderProfile(form.platform) : undefined)
const retainOption = computed(() => ({ value: '', label: t('supplier.retain') }))
const accountModeOptions = computed(() => [
  retainOption.value,
  ...(providerProfile.value?.modes ?? []).map(mode => ({ value: mode.mode, label: providerModeLabel(mode.mode, t) }))
])
const nativeProtocols = computed(() => [...new Set((providerProfile.value?.modes ?? [])
  .filter(mode => !credentials.account_mode || mode.mode === credentials.account_mode)
  .flatMap(mode => Object.keys(mode.base_urls)))])
const protocolOptions = computed(() => [
  retainOption.value,
  ...['adaptive', ...nativeProtocols.value].map(protocol => ({ value: protocol, label: protocol }))
])
const forceGlobalOptions = computed(() => [
  retainOption.value, { value: 'true', label: t('common.yes') }, { value: 'false', label: t('common.no') }
])
const protocolURLs = reactive<Record<string, string>>({})
function protocolURLOptions(protocol: string) {
  return [{ value: '', label: t('supplier.unspecifiedEndpoint') }, ...officialBaseURLs(form.platform, credentials.account_mode, protocol)
    .map(url => ({ value: url, label: url }))]
}
const authModeOptions = computed(() => [
  { value: '', label: t('supplier.retain') },
  { value: 'sigv4', label: 'AWS SigV4' }, { value: 'apikey', label: 'API Key' }
])
const proxyOptions = computed(() => [
  { value: '', label: t('supplier.retainProxy') },
  { value: '0', label: t('supplier.detachProxy') },
  ...(editing.value?.proxy_id && !proxies.value.some(p => p.id === editing.value?.proxy_id)
    ? [{ value: String(editing.value.proxy_id), label: `#${editing.value.proxy_id}` }] : []),
  ...proxies.value.map(proxy => ({ value: String(proxy.id), label: `${proxy.name} · #${proxy.id} · ${proxy.status}` }))
])
const fields = computed(() => {
  if (!supported.value) return []
  let required: string[] = []
  let optional: string[] = []
  switch (form.type) {
    case 'apikey':
      required = ['api_key']
      optional = providerProfile.value ? ['account_mode', 'api_protocol', 'base_url'] : ['base_url']
      break
    case 'upstream': required = ['api_key', 'base_url']; break
    case 'oauth':
      required = ['access_token']
      optional = ['refresh_token', 'id_token', 'expires_at', 'token_type', 'scope', 'email']
      if (form.platform === 'openai') optional.push('chatgpt_account_id', 'chatgpt_user_id', 'organization_id')
      if (form.platform === 'anthropic') optional.push('claude_user_id', 'anthropic_user_id')
      if (['gemini', 'antigravity'].includes(form.platform)) optional.push('project_id')
      break
    case 'setup-token': required = ['access_token']; optional = ['expires_at']; break
    case 'service_account':
      if (vertexAPIKeyEdit.value) { required = ['api_key']; optional = ['location'] }
      else { required = ['service_account_json']; optional = ['project_id', 'location'] }
      break
    case 'bedrock':
      optional = ['aws_force_global']
      required = ['aws_region', 'auth_mode']
      if (credentials.auth_mode === 'apikey') required.push('api_key')
      if (credentials.auth_mode === 'sigv4' || !credentials.auth_mode) {
        required.push('aws_access_key_id', 'aws_secret_access_key')
        optional.push('aws_session_token')
      }
      break
  }
  return [...required.map(key => ({ key, required: true })), ...optional.map(key => ({ key, required: false }))]
})
const columns = computed(() => [
  { key: 'name', label: t('admin.accounts.columns.name') },
  { key: 'platform_type', label: t('admin.accounts.columns.platformType') },
  { key: 'status', label: t('admin.accounts.columns.status') },
  { key: 'usage', label: t('admin.accounts.columns.usageWindows') },
  { key: 'test', label: t('admin.accounts.testConnection') },
  { key: 'last_used_at', label: t('admin.accounts.columns.lastUsed') },
  { key: 'created_at', label: t('admin.accounts.columns.createdAt') },
  { key: 'actions', label: t('common.actions') }
])
const usageAccount = (account: SupplierAccount) => ({
  ...account,
  platform: account.platform as Account['platform'],
  type: account.type as Account['type'],
  status: account.status as Account['status'],
  expires_at: account.expires_at ? Math.floor(new Date(account.expires_at).getTime() / 1000) : null,
  concurrency: 0,
  priority: 0,
  error_message: null,
  last_used_at: account.last_used_at,
  auto_pause_on_expired: false,
  schedulable: account.schedulable,
  rate_limited_at: null,
  rate_limit_reset_at: account.rate_limit_reset_at,
  overload_until: account.overload_until,
  temp_unschedulable_until: account.temp_unschedulable_until,
  temp_unschedulable_reason: null,
  quota_limit: null,
  quota_used: null,
  quota_daily_limit: null,
  quota_daily_used: null,
  quota_weekly_limit: null,
  quota_weekly_used: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null
})
function resetCredentials() {
  Object.keys(protocolURLs).forEach(key => delete protocolURLs[key])
  Object.keys(credentials).forEach(key => delete credentials[key])
  Object.keys(clearCredentials).forEach(key => delete clearCredentials[key])
}
watch(() => form.platform, () => {
  resetCredentials()
})
watch(() => form.type, resetCredentials)
watch(() => credentials.account_mode, () => {
  Object.keys(protocolURLs).forEach(key => delete protocolURLs[key])
  credentials.base_url = ''
})
watch(() => credentials.api_protocol, () => {
  Object.keys(protocolURLs).forEach(key => delete protocolURLs[key])
  credentials.base_url = ''
})

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
async function openCreate() {
  if (createLoading.value) return
  createLoading.value = true
  createProxies.value = []
  try {
    let nextPage = 1
    let pages = 1
    const owned: SupplierProxy[] = []
    do {
      const data = await supplierAPI.proxies.list(nextPage)
      owned.push(...data.items)
      pages = data.pages
      nextPage++
    } while (nextPage <= pages)
    createProxies.value = owned
    showCreate.value = true
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { createLoading.value = false }
}
function open(account: SupplierAccount) {
  if (account.read_only) return
  editing.value = account
  initialExpiry.value = account.expires_at ? new Date(account.expires_at).toISOString().slice(0, 16) : ''
  Object.assign(form, {
    name: account.name, notes: account.notes ?? '', platform: account.platform, type: account.type,
    proxy_id: '', expires_at: initialExpiry.value, clear_expiry: false
  })
  resetCredentials()
  proxyPage.value = 1
  show.value = true
  void loadProxies()
}
function canClear(field: { key: string; required: boolean }) {
  return !field.required && !['base_url', 'account_mode', 'api_protocol', 'aws_force_global', 'project_id', 'location', 'expires_at'].includes(field.key)
}
function isSecret(key: string) {
  return ['api_key', 'access_token', 'refresh_token', 'id_token', 'aws_access_key_id', 'aws_secret_access_key', 'aws_session_token'].includes(key)
}
async function save() {
  if (saving.value || !editing.value || editing.value.read_only) return
  if (!form.name.trim() || new TextEncoder().encode(form.name).length > 100 || new TextEncoder().encode(form.notes).length > 2000) {
    appStore.showError(t('supplier.invalidInput'))
    return
  }
  const changes: SupplierAccountUpdate = {}
  const imported: NonNullable<SupplierAccountUpdate['credentials']> = {}
  for (const field of fields.value) {
    if (clearCredentials[field.key] && canClear(field)) imported[field.key] = ''
    else if (credentials[field.key]) imported[field.key] = credentials[field.key]
  }
  const urls = Object.fromEntries(Object.entries(protocolURLs).filter(([, url]) => url))
  if (Object.keys(urls).length) {
    imported.api_base_urls = urls
    imported.api_protocol = 'adaptive'
    // Match creation: the selected Chat endpoint is also the adaptive fallback.
    if (urls.chat_completions && !imported.base_url) imported.base_url = urls.chat_completions
  }
  if (Object.keys(imported).length) changes.credentials = imported
  if (form.name !== editing.value.name) changes.name = form.name
  if (form.notes !== (editing.value.notes ?? '')) changes.notes = form.notes
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
    await supplierAPI.accounts.update(editing.value.id, changes)
    appStore.showSuccess(t('supplier.saved'))
    show.value = false
    resetCredentials()
    await load()
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { saving.value = false }
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
    <TablePageLayout>
      <template #actions><div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('supplier.accounts') }}</h2>
        <button class="btn btn-primary" :disabled="createLoading" @click="openCreate">{{ t('supplier.addAccount') }}</button>
      </div></template>
      <template #filters><p class="text-sm text-gray-600 dark:text-gray-300">{{ t('supplier.assignmentNotice') }}</p></template>
      <template #table>
        <DataTable :columns="columns" :data="accounts" :loading="loading">
          <template #cell-name="{ row }">
            <div class="font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
            <div class="text-xs text-gray-500 dark:text-gray-400">#{{ row.id }}</div>
            <p v-if="row.notes" :title="row.notes" class="max-w-xs truncate text-xs text-gray-500 dark:text-gray-400">{{ row.notes }}</p>
            <span v-if="row.read_only" class="text-xs text-gray-500 dark:text-gray-400">{{ t('supplier.readOnly') }}</span>
          </template>
          <template #cell-platform_type="{ row }">
            <PlatformTypeBadge :platform="row.platform" :type="row.type" :auth-mode="row.auth_mode" />
          </template>
          <template #cell-status="{ row }">
            <AccountStatusIndicator :account="usageAccount(row)" />
          </template>
          <template #cell-usage="{ row }">
            <AccountUsageCell :account="usageAccount(row)" read-only :usage-fetcher="supplierAPI.accounts.getUsage" />
          </template>
          <template #cell-test="{ row }">
            <button :aria-label="t('admin.accounts.testConnection')" class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400 disabled:opacity-50 disabled:cursor-not-allowed" :disabled="row.read_only" @click="testing = row"><Icon name="play" size="sm" /><span class="text-xs">{{ t('admin.accounts.testConnection') }}</span></button>
          </template>
          <template #cell-last_used_at="{ row }"><span class="text-sm text-gray-500 dark:text-dark-400" :data-testid="`last-used-${row.id}`">{{ row.last_used_at ? formatRelativeTime(row.last_used_at) : '—' }}</span></template>
          <template #cell-created_at="{ value }"><span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) || '—' }}</span></template>
          <template #cell-actions="{ row }">
            <button :aria-label="t('common.edit')" class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400 disabled:opacity-50 disabled:cursor-not-allowed" :disabled="row.read_only" @click="open(row)"><Icon name="edit" size="sm" /><span class="text-xs">{{ t('common.edit') }}</span></button>
          </template>
        </DataTable>
      </template>
      <template #pagination><Pagination v-if="total" :total="total" :page="page" :page-size="20" :show-page-size-selector="false" @update:page="page = $event; load()" /></template>
    </TablePageLayout>
    <CreateAccountModal v-if="showCreate" :show="showCreate" supplier :proxies="createProxies" :groups="[]" @close="showCreate = false" @created="load" />
    <AccountTestModal
      :show="testing !== null"
      :account="testing ? usageAccount(testing) : null"
      :model-loader="supplierAPI.accounts.getModels"
      :test-path="testing ? `/supplier/accounts/${testing.id}/test` : ''"
      diagnostic-only
      @close="testing = null"
    />
    <BaseDialog :show="show" :title="t('supplier.editAccount')" width="normal" @close="close">
      <form id="supplier-account-form" class="space-y-4" @submit.prevent="save">
        <label class="block"><span class="input-label">{{ t('supplier.name') }}</span><input v-model="form.name" name="name" class="input" required maxlength="100" /></label>
        <label class="block"><span class="input-label">{{ t('supplier.notes') }}</span><textarea v-model="form.notes" name="notes" class="input" rows="2" maxlength="2000" /></label>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label for="supplier-account-platform" class="input-label">{{ t('supplier.platform') }}</label>
            <Select id="supplier-account-platform" v-model="form.platform" :options="platformOptions" disabled :aria-label="t('supplier.platform')" />
          </div>
          <div>
            <label for="supplier-account-type" class="input-label">{{ t('supplier.accountType') }}</label>
            <Select id="supplier-account-type" v-model="form.type" :options="typeOptions" disabled :aria-label="t('supplier.accountType')" />
          </div>
        </div>
        <p class="input-hint">{{ t('supplier.importNotice') }}</p>
        <p class="input-hint">{{ t('supplier.retainCredential') }}</p>
        <p v-if="!supported" class="input-hint">{{ t('supplier.unsupportedCredential') }}</p>
        <div v-for="field in fields" :key="field.key" class="space-y-1">
          <label :for="'credential-' + field.key" class="input-label">{{ t(vertexAPIKeyEdit && field.key === 'api_key' ? 'supplier.vertexApiKey' : 'supplier.credential.' + field.key) }} <span class="font-mono text-xs">({{ field.key }})</span></label>
          <Select v-if="field.key === 'base_url'" :id="'credential-' + field.key" v-model="credentials[field.key]" :options="baseURLOptions" :aria-label="t('supplier.credential.' + field.key)" />
          <Select v-else-if="field.key === 'account_mode'" :id="'credential-' + field.key" v-model="credentials[field.key]" :options="accountModeOptions" :aria-label="t('supplier.credential.' + field.key)" />
          <Select v-else-if="field.key === 'api_protocol'" :id="'credential-' + field.key" v-model="credentials[field.key]" :options="protocolOptions" :aria-label="t('supplier.credential.' + field.key)" />
          <Select v-else-if="field.key === 'aws_force_global'" :id="'credential-' + field.key" v-model="credentials[field.key]" :options="forceGlobalOptions" :aria-label="t('supplier.credential.' + field.key)" />
          <Select v-else-if="field.key === 'auth_mode'" :id="'credential-' + field.key" v-model="credentials[field.key]" :options="authModeOptions" :aria-label="t('supplier.credential.' + field.key)" />
          <textarea v-else-if="field.key === 'service_account_json'" :id="'credential-' + field.key" v-model="credentials[field.key]" :name="field.key" class="input font-mono" rows="5" maxlength="32768" :placeholder="t('supplier.serviceAccountHint')" />
          <input v-else :id="'credential-' + field.key" v-model="credentials[field.key]" :name="field.key" :type="isSecret(field.key) ? 'password' : 'text'" class="input" :maxlength="field.key === 'expires_at' ? 64 : 16384" autocomplete="off" :disabled="clearCredentials[field.key]" :placeholder="field.key === 'expires_at' ? t('supplier.tokenExpiryHint') : t('supplier.retainCredential')" />
          <label v-if="canClear(field)" class="flex items-center gap-2 text-sm"><input v-model="clearCredentials[field.key]" type="checkbox" />{{ t('supplier.clearCredential', { field: field.key }) }}</label>
        </div>
        <div v-if="providerProfile && (!credentials.api_protocol || credentials.api_protocol === 'adaptive')" class="space-y-3">
          <p class="input-hint">{{ t('supplier.protocolEndpointsHint') }}</p>
          <div v-for="protocol in nativeProtocols" :key="protocol">
            <label :for="'credential-api_base_urls-' + protocol" class="input-label">{{ protocol }}</label>
            <Select :id="'credential-api_base_urls-' + protocol" v-model="protocolURLs[protocol]" :options="protocolURLOptions(protocol)" :aria-label="protocol" />
          </div>
        </div>
        <p v-if="form.type === 'service_account' && !vertexAPIKeyEdit" class="input-hint">{{ t('supplier.serviceAccountNotice') }}</p>
        <p class="input-hint">{{ t('supplier.endpointPolicy') }}</p>
        <div>
          <label for="supplier-account-proxy" class="input-label">{{ t('supplier.proxy') }}</label>
          <Select id="supplier-account-proxy" v-model="form.proxy_id" :options="proxyOptions" :disabled="proxyLoading" :aria-label="t('supplier.proxy')" />
        </div>
        <Pagination v-if="proxyTotal > 20" :total="proxyTotal" :page="proxyPage" :page-size="20" :show-page-size-selector="false" @update:page="proxyPage = $event; loadProxies()" />
        <label class="block"><span class="input-label">{{ t('supplier.expiryUTC') }}</span><input v-model="form.expires_at" name="expires_at" type="datetime-local" class="input" :disabled="form.clear_expiry" min="1970-01-01T00:00" max="9999-12-31T23:59" /></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="form.clear_expiry" name="clear_expiry" type="checkbox" />{{ t('supplier.clearExpiry') }}</label>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button form="supplier-account-form" type="submit" class="btn btn-primary" :disabled="saving">{{ t('common.save') }}</button></template>
    </BaseDialog>
  </AppLayout>
</template>
