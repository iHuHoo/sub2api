<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import { supplierAPI, supplierErrorKey, type SupplierUsage, type SupplierUsageQuery } from '@/api/supplier'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const today = new Date()
const endDate = today.toISOString().slice(0, 10)
const startDate = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - 29)).toISOString().slice(0, 10)
const form = reactive({ start_date: startDate, end_date: endDate, account_id: '' })
const usage = ref<SupplierUsage | null>(null)
const loading = ref(false)
const metrics = ['requests', 'input_tokens', 'output_tokens', 'cache_creation_tokens', 'cache_read_tokens'] as const
const columns = computed(() => [{ key: 'date', label: t('supplier.dateUTC') }, ...metrics.map(key => ({ key, label: t('supplier.metrics.' + key) }))])
async function load() {
  if (loading.value) return
  const start = Date.parse(form.start_date + 'T00:00:00Z')
  const end = Date.parse(form.end_date + 'T00:00:00Z')
  const accountID = form.account_id === '' ? undefined : Number(form.account_id)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start || (end - start) / 86400000 >= 366 || (accountID !== undefined && (!Number.isSafeInteger(accountID) || accountID <= 0))) {
    appStore.showError(t('supplier.invalidRange'))
    return
  }
  const query: SupplierUsageQuery = { start_date: form.start_date, end_date: form.end_date }
  if (accountID !== undefined) query.account_id = accountID
  loading.value = true
  usage.value = null
  try { usage.value = await supplierAPI.usage(query) }
  catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { loading.value = false }
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="space-y-5">
      <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('supplier.usage') }}</h2>
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('supplier.usageNotice') }}</p>
      <form class="card flex flex-wrap items-end gap-4 p-4" @submit.prevent="load">
        <label class="block"><span class="input-label">{{ t('supplier.startDate') }}</span><input v-model="form.start_date" name="start_date" type="date" class="input" required /></label>
        <label class="block"><span class="input-label">{{ t('supplier.endDate') }}</span><input v-model="form.end_date" name="end_date" type="date" class="input" required /></label>
        <label class="block"><span class="input-label">{{ t('supplier.accountID') }}</span><input v-model="form.account_id" name="account_id" type="number" min="1" step="1" class="input" :placeholder="t('supplier.allAccounts')" /></label>
        <button class="btn btn-primary" type="submit" :disabled="loading">{{ t('supplier.loadUsage') }}</button>
        <p class="input-hint w-full">{{ t('supplier.accountFilterNotice') }}</p>
      </form>
      <div v-if="usage" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <div v-for="key in metrics" :key="key" class="card p-4"><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('supplier.metrics.' + key) }}</p><p class="mt-2 text-xl font-semibold">{{ usage.summary[key].toLocaleString() }}</p></div>
      </div>
      <div class="card overflow-hidden"><DataTable :data="usage?.daily ?? []" :columns="columns" :loading="loading" /></div>
    </div>
  </AppLayout>
</template>
