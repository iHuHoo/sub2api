<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMutationObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend, type ChartData, type ChartOptions } from 'chart.js'
import { Line } from 'vue-chartjs'
import type { SupplierUsage } from '@/api/supplier'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)
const props = defineProps<{ daily: SupplierUsage['daily']; updatedAt: string | null }>()
const { t } = useI18n()
const dark = ref(document.documentElement.classList.contains('dark'))
useMutationObserver(document.documentElement, () => {
  dark.value = document.documentElement.classList.contains('dark')
}, { attributes: true, attributeFilter: ['class'] })
const colors = computed(() => ({ text: dark.value ? '#d1d5db' : '#4b5563', grid: dark.value ? '#374151' : '#e5e7eb' }))
const updatedAtUTC = computed(() => props.updatedAt ? new Date(props.updatedAt).toISOString().slice(0, 19).replace('T', ' ') + ' UTC' : null)
const chartData = computed<ChartData<'line'>>(() => ({
  labels: props.daily.map(day => day.date),
  datasets: [
    { label: t('supplier.usageStats.spend'), yAxisID: 'cost', data: props.daily.map(day => Number(day.cost)), borderColor: '#3b82f6' },
    { label: t('supplier.usageStats.tokens'), yAxisID: 'tokens', data: props.daily.map(day => day.tokens), borderColor: '#10b981' },
    { label: t('supplier.usageStats.enabled'), yAxisID: 'accounts', data: props.daily.map(day => day.enabled_accounts), borderColor: '#f59e0b' }
  ].map(dataset => ({ ...dataset, cubicInterpolationMode: 'monotone', spanGaps: false, pointRadius: 0, pointHoverRadius: 4, borderWidth: 2 }))
}))
const options = computed<ChartOptions<'line'>>(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' },
  plugins: {
    legend: { labels: { color: colors.value.text, usePointStyle: true, boxWidth: 8, boxHeight: 8 } },
    tooltip: { callbacks: {
      label: context => {
        const day = props.daily[context.dataIndex]
        const value = context.datasetIndex === 0 ? '$' + day.cost
          : context.datasetIndex === 1 ? day.tokens.toLocaleString() : day.enabled_accounts?.toLocaleString() ?? '—'
        return context.dataset.label + ': ' + value
      }
    } }
  },
  scales: {
    x: { grid: { display: false }, ticks: { color: colors.value.text, autoSkip: true, maxTicksLimit: 8, maxRotation: 0, callback: (_, index) => props.daily[index]?.date.slice(5) } },
    cost: { type: 'linear', position: 'left', beginAtZero: true, title: { display: true, text: t('supplier.usageStats.spend') + ' ($)', color: colors.value.text }, grid: { color: colors.value.grid }, ticks: { color: colors.value.text } },
    tokens: { type: 'linear', position: 'right', beginAtZero: true, title: { display: true, text: t('supplier.usageStats.tokens'), color: colors.value.text }, grid: { drawOnChartArea: false }, ticks: { color: colors.value.text } },
    accounts: { type: 'linear', display: false, beginAtZero: true, ticks: { precision: 0 } }
  }
}))
</script>

<template>
  <section class="card min-w-0 p-4">
    <div class="mb-4 flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('supplier.usageStats.trend') }}</h3>
      <p v-if="updatedAtUTC" class="text-xs text-gray-500 dark:text-gray-400">{{ t('supplier.usageStats.updatedAt') }}: {{ updatedAtUTC }}</p>
    </div>
    <div class="relative h-72 min-w-0">
      <Line :data="chartData" :options="options" role="img" :aria-label="t('supplier.usageStats.trend')" />
    </div>
  </section>
</template>
