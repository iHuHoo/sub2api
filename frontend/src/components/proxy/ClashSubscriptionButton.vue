<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { clashSubscriptionAPI } from '@/api/clashSubscription'
import ClashSubscriptionDialog from './ClashSubscriptionDialog.vue'
const props = defineProps<{ role: 'admin' | 'supplier' }>()
const { t } = useI18n()
const enabled = ref(false)
const show = ref(false)
onMounted(async () => {
 try { await clashSubscriptionAPI(props.role).metadata(); enabled.value = true } catch { enabled.value = false }
})
</script>
<template>
 <button v-if="enabled" class="btn btn-secondary" @click="show = true">{{ t('clash.title') }}</button>
 <ClashSubscriptionDialog :show="show" :role="role" @close="show = false" />
</template>
