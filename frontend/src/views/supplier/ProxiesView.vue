<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import { supplierAPI, supplierErrorKey, type SupplierProxy, type SupplierProxyInput } from '@/api/supplier'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const proxies = ref<SupplierProxy[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const saving = ref(false)
const show = ref(false)
const editing = ref<SupplierProxy | null>(null)
const deleting = ref<SupplierProxy | null>(null)
const testing = ref<number | null>(null)
const results = reactive<Record<number, { reachable: boolean; latency_ms: number }>>({})
const form = reactive({ name: '', protocol: 'http' as NonNullable<SupplierProxyInput['protocol']>, host: '', port: 8080, username: '', password: '', clear_password: false, status: 'active' as 'active' | 'inactive' })
const columns = computed(() => [
  { key: 'name', label: t('supplier.name') }, { key: 'address', label: t('supplier.address') },
  { key: 'status', label: t('supplier.lifecycle') }, { key: 'has_password', label: t('supplier.authentication') },
  { key: 'actions', label: t('common.actions') }
])
async function load() {
  loading.value = true
  try {
    const data = await supplierAPI.proxies.list(page.value)
    proxies.value = data.items
    total.value = data.total
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { loading.value = false }
}
function open(proxy: SupplierProxy | null = null) {
  editing.value = proxy
  Object.assign(form, {
    name: proxy?.name ?? '', protocol: proxy?.protocol ?? 'http', host: proxy?.host ?? '', port: proxy?.port ?? 8080,
    username: proxy?.username ?? '', password: '', clear_password: false, status: proxy?.status ?? 'active'
  })
  show.value = true
}
async function save() {
  if (saving.value) return
  if (!form.name.trim() || !Number.isInteger(form.port) || form.port < 1 || form.port > 65535) {
    appStore.showError(t('supplier.invalidInput'))
    return
  }
  const input: SupplierProxyInput = {}
  for (const key of ['name', 'protocol', 'host', 'port', 'username', 'status'] as const) {
    if (!editing.value || form[key] !== editing.value[key]) Object.assign(input, { [key]: form[key] })
  }
  if (form.clear_password) input.password = ''
  else if (form.password) input.password = form.password
  saving.value = true
  try {
    if (editing.value) {
      await supplierAPI.proxies.update(editing.value.id, input)
      delete results[editing.value.id]
    } else await supplierAPI.proxies.create(input)
    appStore.showSuccess(t('supplier.saved'))
    show.value = false
    form.password = ''
    await load()
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { saving.value = false }
}
async function test(proxy: SupplierProxy) {
  if (testing.value !== null) return
  testing.value = proxy.id
  delete results[proxy.id]
  try { results[proxy.id] = await supplierAPI.proxies.test(proxy.id) }
  catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { testing.value = null }
}
async function remove() {
  if (!deleting.value || saving.value) return
  saving.value = true
  try {
    await supplierAPI.proxies.delete(deleting.value.id)
    delete results[deleting.value.id]
    deleting.value = null
    if (proxies.value.length === 1 && page.value > 1) page.value--
    await load()
  } catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { saving.value = false }
}
function close() {
  if (saving.value) return
  show.value = false
  form.password = ''
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('supplier.proxies') }}</h2><button class="btn btn-primary" @click="open()">{{ t('supplier.addProxy') }}</button></div>
      <p class="card p-4 text-sm text-gray-600 dark:text-gray-300">{{ t('supplier.proxyPolicy') }} {{ t('supplier.tcpNotice') }}</p>
      <div class="card overflow-hidden">
        <DataTable :columns="columns" :data="proxies" :loading="loading">
          <template #cell-name="{ row }">{{ row.name }} <span class="text-xs text-gray-500">#{{ row.id }}</span></template>
          <template #cell-address="{ row }"><span class="break-all font-mono text-sm">{{ row.protocol }}://{{ row.host.includes(':') ? '[' + row.host + ']' : row.host }}:{{ row.port }}</span></template>
          <template #cell-status="{ value }">{{ value === 'active' ? t('common.active') : t('supplier.inactive') }}</template>
          <template #cell-has_password="{ row }"><span>{{ row.username || '—' }} · {{ row.has_password ? t('supplier.passwordSaved') : t('supplier.noPassword') }}</span></template>
          <template #cell-actions="{ row }">
            <div class="flex flex-wrap gap-2"><button class="btn btn-secondary btn-sm" @click="open(row)">{{ t('common.edit') }}</button><button class="btn btn-secondary btn-sm" :disabled="testing !== null" @click="test(row)">{{ t('supplier.tcpTest') }}</button><button class="btn btn-secondary btn-sm" @click="deleting = row">{{ t('common.delete') }}</button></div>
            <p v-if="results[row.id]" role="status" class="mt-1 text-sm">{{ results[row.id].reachable ? t('supplier.reachable') : t('supplier.unreachable') }} · {{ results[row.id].latency_ms }} ms</p>
          </template>
        </DataTable>
        <Pagination v-if="total" :total="total" :page="page" :page-size="20" :show-page-size-selector="false" @update:page="page = $event; load()" />
      </div>
    </div>
    <BaseDialog :show="show" :title="editing ? t('supplier.editProxy') : t('supplier.addProxy')" width="normal" @close="close">
      <form id="supplier-proxy-form" class="space-y-4" @submit.prevent="save">
        <label class="block"><span class="input-label">{{ t('supplier.name') }}</span><input v-model="form.name" name="name" class="input" required maxlength="100" /></label>
        <label class="block"><span class="input-label">{{ t('supplier.protocol') }}</span><select v-model="form.protocol" name="protocol" class="input"><option v-for="protocol in ['http', 'https', 'socks5', 'socks5h']" :key="protocol" :value="protocol">{{ protocol }}</option></select></label>
        <label class="block"><span class="input-label">{{ t('supplier.publicIP') }}</span><input v-model="form.host" name="host" class="input font-mono" required /><span class="input-hint">{{ t('supplier.proxyPolicy') }}</span></label>
        <label class="block"><span class="input-label">{{ t('supplier.port') }}</span><input v-model.number="form.port" name="port" type="number" class="input" min="1" max="65535" required /></label>
        <label class="block"><span class="input-label">{{ t('supplier.username') }}</span><input v-model="form.username" name="username" class="input" maxlength="255" autocomplete="off" /></label>
        <label class="block"><span class="input-label">{{ t('supplier.password') }}</span><input v-model="form.password" name="password" type="password" class="input" maxlength="255" autocomplete="new-password" :disabled="form.clear_password" :placeholder="editing ? t('supplier.retainCredential') : ''" /></label>
        <label v-if="editing" class="flex items-center gap-2 text-sm"><input v-model="form.clear_password" name="clear_password" type="checkbox" />{{ t('supplier.clearPassword') }}</label>
        <label class="block"><span class="input-label">{{ t('supplier.lifecycle') }}</span><select v-model="form.status" name="status" class="input"><option value="active">{{ t('common.active') }}</option><option value="inactive">{{ t('supplier.inactive') }}</option></select></label>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button form="supplier-proxy-form" type="submit" class="btn btn-primary" :disabled="saving">{{ t('common.save') }}</button></template>
    </BaseDialog>
    <BaseDialog :show="!!deleting" :title="t('common.delete')" width="normal" @close="!saving && (deleting = null)">
      <p class="text-sm">{{ t('supplier.deleteProxyNotice', { name: deleting?.name }) }}</p>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="deleting = null">{{ t('common.cancel') }}</button><button class="btn btn-danger" :disabled="saving" @click="remove">{{ t('supplier.confirmDeletion') }}</button></template>
    </BaseDialog>
  </AppLayout>
</template>
