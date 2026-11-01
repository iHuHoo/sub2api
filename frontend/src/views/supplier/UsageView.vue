<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import SupplierUsageTrend from '@/components/charts/SupplierUsageTrend.vue'
import { supplierAPI, supplierErrorKey, type SupplierUsage } from '@/api/supplier'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const usage = ref<SupplierUsage | null>(null)
const loading = ref(true)
const accounts = computed(() => [...(usage.value?.per_account ?? [])].sort((a, b) => a.id - b.id))
const statusClasses = {
  enabled: 'text-green-700 dark:text-green-400',
  pending: 'text-amber-700 dark:text-amber-400',
  error: 'text-red-600 dark:text-red-400',
  deleted: 'text-gray-500 dark:text-gray-400'
}
function formatMoney(value: string) {
  const [whole, fraction = ''] = value.split('.')
  const decimals = fraction.replace(/0+$/, '')
  return '$' + whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (decimals ? '.' + decimals : '')
}
async function load() {
  try { usage.value = await supplierAPI.usage() }
  catch (error) { appStore.showError(t(supplierErrorKey(error))) }
  finally { loading.value = false }
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="min-w-0 space-y-6">
      <div>
        <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('supplier.usage') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.utc') }} · {{ t('supplier.usageStats.refresh') }}</p>
      </div>
      <div v-if="loading" class="flex justify-center py-12"><LoadingSpinner /></div>
      <div v-else-if="usage && !usage.ready" role="status" class="card p-4 text-gray-600 dark:text-gray-300">
        {{ t('supplier.usageStats.inProgress') }}
      </div>
      <template v-else-if="usage">
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div class="card min-w-0 p-4" data-testid="spend-card">
            <div class="flex items-start gap-3">
              <div class="hidden shrink-0 rounded-lg bg-blue-100 p-2 dark:bg-blue-900/30 sm:block"><Icon name="dollar" size="md" class="text-blue-600 dark:text-blue-400" /></div>
              <div class="min-w-0">
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.spend') }}</p>
                <p class="mt-1 break-words text-xl font-bold text-gray-900 dark:text-white">{{ t('supplier.usageStats.total') }}: {{ formatMoney(usage.cost.total) }}</p>
                <p class="mt-1 break-words text-xs text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.today') }}: {{ formatMoney(usage.cost.today) }}</p>
              </div>
            </div>
          </div>
          <div class="card min-w-0 p-4" data-testid="accounts-card">
            <div class="flex items-start gap-3">
              <div class="hidden shrink-0 rounded-lg bg-purple-100 p-2 dark:bg-purple-900/30 sm:block"><Icon name="server" size="md" class="text-purple-600 dark:text-purple-400" /></div>
              <div class="min-w-0">
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.accounts') }}</p>
                <p class="mt-1 break-words text-xl font-bold text-gray-900 dark:text-white">{{ usage.accounts.total.toLocaleString() }}</p>
                <div class="mt-1 flex flex-wrap gap-x-2 gap-y-1 text-xs">
                  <span v-for="status in (['enabled', 'pending', 'error'] as const)" :key="status" :class="statusClasses[status]">{{ usage.accounts[status].toLocaleString() }} {{ t('supplier.usageStats.' + status) }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-for="metric in (['tokens', 'requests'] as const)" :key="metric" class="card min-w-0 p-4" :data-testid="metric + '-card'">
            <div class="flex items-start gap-3">
              <div class="hidden shrink-0 rounded-lg p-2 sm:block" :class="metric === 'tokens' ? 'bg-amber-100 dark:bg-amber-900/30' : 'bg-green-100 dark:bg-green-900/30'">
                <Icon :name="metric === 'tokens' ? 'cube' : 'chart'" size="md" :class="metric === 'tokens' ? 'text-amber-600 dark:text-amber-400' : 'text-green-600 dark:text-green-400'" />
              </div>
              <div class="min-w-0">
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.' + metric) }}</p>
                <p class="mt-1 break-words text-xl font-bold text-gray-900 dark:text-white">{{ t('supplier.usageStats.total') }}: {{ usage[metric].total.toLocaleString() }}</p>
                <p class="mt-1 break-words text-xs text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.today') }}: {{ usage[metric].today.toLocaleString() }}</p>
              </div>
            </div>
          </div>
        </div>
        <SupplierUsageTrend :daily="usage.daily" :updated-at="usage.updated_at" />
        <section class="card min-w-0 p-4">
          <h3 id="supplier-account-spend" class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('supplier.usageStats.perAccount') }}</h3>
          <div class="overflow-x-auto" role="region" aria-labelledby="supplier-account-spend" tabindex="0">
            <table class="w-full min-w-max divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
                <tr>
                  <th scope="col" class="px-4 py-3 text-left">{{ t('supplier.usageStats.accounts') }}</th>
                  <th scope="col" class="px-4 py-3 text-left">{{ t('supplier.lifecycle') }}</th>
                  <th v-for="key in ['today', 'last31', 'total']" :key="key" scope="col" class="px-4 py-3 text-right">{{ t('supplier.usageStats.' + key) }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-200 text-gray-900 dark:divide-dark-700 dark:text-gray-100">
                <tr v-for="account in accounts" :key="account.id" class="hover:bg-gray-50 dark:hover:bg-dark-800">
                  <td class="px-4 py-3">#{{ account.id }} {{ account.name }}</td>
                  <td class="px-4 py-3" :class="statusClasses[account.deleted ? 'deleted' : account.status]">{{ t('supplier.usageStats.' + (account.deleted ? 'deleted' : account.status)) }}</td>
                  <td class="px-4 py-3 text-right tabular-nums">{{ formatMoney(account.today_cost) }}</td>
                  <td class="px-4 py-3 text-right tabular-nums">{{ formatMoney(account.last31_cost) }}</td>
                  <td class="px-4 py-3 text-right tabular-nums">{{ formatMoney(account.total_cost) }}</td>
                </tr>
                <tr v-if="!accounts.length"><td colspan="5" class="px-4 py-8 text-center text-gray-500 dark:text-gray-400">{{ t('empty.noData') }}</td></tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>
