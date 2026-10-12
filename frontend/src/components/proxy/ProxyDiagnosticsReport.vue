<template>
  <p v-if="report.schema_version !== 1" role="alert" class="text-red-600">{{ t(k + 'errors.schema_unsupported') }}</p>
  <section v-else class="min-w-0 space-y-4 text-sm" data-test="diagnostics-report">
    <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
      <div class="flex flex-wrap items-center gap-2">
        <span class="badge" :class="statusClass(report.connectivity)">{{ t(k + 'connectivity.' + report.connectivity) }}</span>
        <span class="badge badge-info">{{ t(k + report.execution) }}</span>
        <span>{{ t(k + report.mode) }}</span>
        <span v-if="!saved" class="badge badge-warning">{{ t(k + 'notSaved') }}</span>
      </div>
      <p class="mt-2 break-words">{{ report.summary }}</p>
      <p class="mt-2 text-gray-500 dark:text-gray-400">{{ t(k + 'noModelCalls') }}</p>
      <dl class="mt-3 grid gap-2 sm:grid-cols-2">
        <div><dt class="text-gray-500">{{ t(k + 'checkedAt') }}</dt><dd>{{ formatDateTime(report.checked_at) }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'completion') }}</dt><dd>{{ t(k + 'counts', report.completion) }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'requestId') }}</dt><dd class="break-all font-mono text-xs">{{ report.request_id }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'ruleset') }}</dt><dd class="break-all font-mono text-xs">{{ report.ruleset_version }}</dd></div>
      </dl>
    </div>
    <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
      <h3 class="font-semibold">{{ t(k + 'exitTitle') }}</h3>
      <dl v-if="report.exit" class="mt-2 grid gap-2 sm:grid-cols-2">
        <div v-for="field in exitFields" :key="field"><dt class="text-gray-500">{{ t(k + 'exit.' + field) }}</dt><dd class="break-all">{{ field === 'observed_at' ? formatDateTime(report.exit[field]) : report.exit[field] || '—' }}</dd></div>
      </dl>
      <p v-else class="mt-2 text-gray-500">{{ t(k + 'noExit') }}</p>
    </div>
    <article v-for="item in report.items" :key="item.id" class="min-w-0 rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-test="diagnostic-item" :data-status="item.status">
      <div class="flex flex-wrap items-center justify-between gap-2"><h3 class="break-all font-semibold">{{ item.id }}</h3><span class="badge" :class="statusClass(item.status)">{{ statusLabel(item.status) }}</span></div>
      <p class="mt-2 break-all font-mono text-xs">{{ item.target }}</p>
      <p class="mt-2 break-words">{{ item.message }}</p>
      <p class="mt-1 break-all text-gray-500">{{ reasonLabel(item.reason_code) }} <span v-if="knownReasons.has(item.reason_code)">({{ item.reason_code }})</span></p>
      <dl class="mt-3 grid gap-2 text-xs sm:grid-cols-2">
        <div><dt class="text-gray-500">{{ t(k + 'plugin') }}</dt><dd class="break-all">{{ item.plugin_id }} · {{ item.plugin_version }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'rule') }}</dt><dd class="break-all">{{ item.rule_id }} · {{ item.rule_version }} · {{ item.category }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'startedAt') }}</dt><dd>{{ formatDateTime(item.started_at) }}</dd></div>
        <div><dt class="text-gray-500">{{ t(k + 'duration') }}</dt><dd>{{ milliseconds(item.duration_ms) }}<span v-if="item.http_status"> · HTTP {{ item.http_status }}</span></dd></div>
        <div v-if="item.cf_ray"><dt>CF-Ray</dt><dd class="break-all">{{ item.cf_ray }}</dd></div>
        <div v-for="[key, value] in safeEvidence(item.evidence)" :key="key"><dt class="text-gray-500">{{ key }}</dt><dd class="break-all">{{ value }}</dd></div>
        <div v-if="item.exit"><dt class="text-gray-500">{{ t(k + 'exitTitle') }}</dt><dd class="break-all">{{ item.exit.ip }} · {{ item.exit.source }} · {{ formatDateTime(item.exit.observed_at) }}</dd></div>
      </dl>
    </article>
    <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
      <h3 class="font-semibold">{{ t(k + 'stability') }} · {{ statusLabel(report.stability.status) }}</h3>
      <p class="mt-2 break-all text-gray-500">{{ reasonLabel(report.stability.reason_code) }}</p>
      <p class="mt-2">{{ t(k + 'sampling', report.stability) }}</p>
      <p>{{ t(k + 'latencies') }}: {{ milliseconds(report.stability.latency_min_ms) }} / {{ milliseconds(report.stability.latency_median_ms) }} / {{ milliseconds(report.stability.latency_max_ms) }}</p>
      <div v-for="group in sampleGroups" :key="group.id" class="mt-3">
        <h4 class="break-all font-medium">{{ group.id }}</h4>
        <ul class="space-y-2">
          <li v-for="(sample, index) in group.samples" :key="index" class="break-all rounded bg-gray-50 p-2 dark:bg-dark-700">
            #{{ index + 1 }} · {{ statusLabel(sample.status) }} · {{ reasonLabel(sample.reason_code) }}
            <div>{{ sample.ip || '—' }} · {{ milliseconds(sample.latency_ms) }} · {{ sample.source || '—' }}</div>
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProxyDiagnosticsReport } from '@/types'
import { formatDateTime } from '@/utils/format'
const props = defineProps<{ report: ProxyDiagnosticsReport; saved: boolean }>()
const { t, tm } = useI18n()
const k = 'admin.proxies.diagnostics.'
const knownReasons = computed(() => new Set(Object.keys(tm(k + 'reasons'))))
const exitFields = ['ip', 'country_code', 'country', 'region', 'city', 'asn', 'organization', 'source', 'observed_at'] as const
const safeEvidence = (evidence?: Record<string, unknown>) => Object.entries(evidence ?? {}).filter(([, value]) => ['string', 'number', 'boolean'].includes(typeof value))
const milliseconds = (value?: number) => typeof value === 'number' ? `${value} ms` : '—'
const statusClass = (status: string) => ({ pass: 'badge-success', reachable: 'badge-success', fail: 'badge-danger', unusable: 'badge-danger', warn: 'badge-warning', challenge: 'badge-warning', degraded: 'badge-warning' } as Record<string, string>)[status] ?? 'badge-gray'
const statusLabel = (status: string) => t(k + 'statuses.' + (['pass', 'warn', 'fail', 'challenge', 'unknown', 'skipped'].includes(status) ? status : 'unknown'))
const reasonLabel = (reason: string) => knownReasons.value.has(reason) ? t(k + 'reasons.' + reason) : `${t(k + 'unknownReason')}: ${reason}`
const sampleGroups = computed(() => [
  { id: t(k + 'stability'), samples: props.report.stability.samples },
  ...props.report.items.filter(item => item.category !== 'stability' && item.samples?.length).map(item => ({ id: item.id, samples: item.samples! }))
].filter(group => group.samples.length))
</script>
