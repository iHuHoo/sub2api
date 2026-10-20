<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { clashSubscriptionAPI, type ClashMetadata } from '@/api/clashSubscription'

const props = defineProps<{ show: boolean; role: 'admin' | 'supplier' }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const metadata = ref<ClashMetadata | null>(null)
const link = ref('')
const days = ref(90)
const busy = ref(false)
const message = ref('')
const confirm = ref<'reset' | 'revoke' | null>(null)
let generation = 0
const available = computed(() => !busy.value && !!metadata.value?.exportable_count)
const current = (ticket: number) => props.show && ticket === generation
function clear() { generation++; link.value = ''; metadata.value = null; message.value = ''; confirm.value = null; busy.value = false }
function close() { clear(); emit('close') }
async function refresh(ticket: number) {
 const value = await clashSubscriptionAPI(props.role).metadata()
 if (!current(ticket)) return false
 if (metadata.value && metadata.value.version !== value.version) link.value = ''
 metadata.value = value
 return true
}
watch(() => props.show, async show => {
 clear()
 if (!show) return
 const ticket = generation
 busy.value = true
 try { await refresh(ticket) } catch { if (current(ticket)) message.value = t('clash.unavailable') }
 finally { if (current(ticket)) busy.value = false }
}, { immediate: true })
onBeforeUnmount(clear)
async function act(action: 'create' | 'reset' | 'revoke' | 'copy') {
 if (busy.value) return
 const ticket = generation
 const expected = metadata.value?.version
 busy.value = true
 message.value = ''
 try {
  if (!await refresh(ticket) || !metadata.value) return
  if (metadata.value.version !== expected) { link.value = ''; message.value = t('clash.conflict'); return }
  if (action !== 'revoke' && metadata.value.exportable_count === 0) { message.value = t('clash.zero'); return }
  const api = clashSubscriptionAPI(props.role)
  if (action === 'copy') { if (link.value) { await navigator.clipboard.writeText(link.value); if (current(ticket)) message.value = t('clash.copied') } }
  else if (action === 'revoke') { await api.revoke(metadata.value.version); if (current(ticket)) { link.value = ''; await refresh(ticket) } }
  else {
   link.value = ''
   const grant = action === 'create' ? await api.create(days.value) : await api.reset(metadata.value.version, days.value)
   if (current(ticket)) { metadata.value = grant.metadata; link.value = grant.subscription_url }
  }
 } catch {
  if (current(ticket)) {
   link.value = ''; message.value = t('clash.retry')
   try { await refresh(ticket) } catch { /* Preserve the explicit retry message. */ }
  }
 } finally { if (current(ticket)) { busy.value = false; confirm.value = null } }
}
</script>

<template>
 <BaseDialog :show="show" :title="t('clash.title')" @close="close">
  <div class="space-y-4">
   <p>{{ t(role === 'supplier' ? 'clash.supplierScope' : 'clash.adminScope') }}</p>
   <p class="rounded-lg bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-100">{{ t('clash.warning') }}</p>
   <template v-if="metadata">
    <p>{{ t('clash.count', { count: metadata.exportable_count, skipped: metadata.skipped_count }) }}</p>
    <p>{{ t(metadata.active ? 'clash.active' : metadata.exists ? 'clash.inactive' : 'clash.absent') }}</p>
    <p v-if="metadata.expires_at">{{ t('clash.expires') }}: {{ new Date(metadata.expires_at).toLocaleString() }}</p>
    <p v-if="metadata.exportable_count === 0">{{ t('clash.zero') }}</p>
    <label class="block">{{ t('clash.duration') }}
     <select v-model="days" class="input mt-1" :disabled="busy"><option v-for="n in [30, 90, 365]" :key="n" :value="n">{{ t('clash.days', { count: n }) }}</option></select>
    </label>
    <div v-if="link" class="space-y-2"><p>{{ t('clash.once') }}</p><input data-test="link" :value="link" class="input" readonly autocomplete="off" :aria-label="t('clash.title')" /><button class="btn btn-secondary" :disabled="!available" @click="act('copy')">{{ t('clash.copy') }}</button></div>
    <p v-else-if="metadata.exists">{{ t('clash.noRecover') }}</p>
    <p class="text-sm">{{ t('clash.import') }}</p>
   </template>
   <p v-if="message" role="status">{{ message }}</p>
   <p v-if="confirm" role="alert">{{ t('clash.confirm') }}</p>
  </div>
  <template #footer>
   <button class="btn btn-secondary" @click="close">{{ t('clash.close') }}</button>
   <template v-if="metadata && !confirm">
    <button v-if="!metadata.exists" data-test="create" class="btn btn-primary" :disabled="!available" @click="act('create')">{{ t('clash.create') }}</button>
    <button v-else class="btn btn-primary" :disabled="!available" @click="confirm = 'reset'">{{ t('clash.reset') }}</button>
    <button v-if="metadata.exists" class="btn btn-danger" :disabled="busy" @click="confirm = 'revoke'">{{ t('clash.revoke') }}</button>
   </template>
   <button v-if="confirm" class="btn btn-danger" :disabled="busy" @click="act(confirm)">{{ t('clash.proceed') }}</button>
  </template>
 </BaseDialog>
</template>
