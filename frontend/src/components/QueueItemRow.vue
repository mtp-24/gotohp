<script setup lang="ts">
import { computed } from 'vue'
import type { QueueItem, ThreadStatus } from '../utils/UploadManager'
import { RotateCcw, X } from '@lucide/vue'

const props = defineProps<{
  item: QueueItem
  thread?: ThreadStatus
}>()

defineEmits<{
  cancel: []
  retry: []
}>()

const stateConfig = computed(() => {
  switch (props.item.State) {
    case 'queued':
      return { color: 'text-muted-foreground', bg: 'bg-muted', label: 'Queued' }
    case 'active':
      return { color: 'text-blue-500', bg: 'bg-blue-500/10', label: 'Active' }
    case 'done':
      return { color: 'text-primary', bg: 'bg-primary/10', label: 'Done' }
    case 'failed':
      return { color: 'text-destructive', bg: 'bg-destructive/10', label: 'Failed' }
    case 'skipped':
      return { color: 'text-amber-700 dark:text-amber-300', bg: 'bg-amber-500/10', label: 'Skipped' }
    default:
      return { color: 'text-muted-foreground', bg: 'bg-muted', label: 'Cancelled' }
  }
})

const uploadPercent = computed(() => {
  const thread = props.thread
  if (!thread || thread.Status !== 'uploading' || !thread.BytesTotal) return null
  return Math.round((thread.BytesUploaded / thread.BytesTotal) * 100)
})

const detail = computed(() => {
  if (props.item.State === 'active' && props.thread) {
    return uploadPercent.value !== null ? `${props.thread.Message} ${uploadPercent.value}%` : props.thread.Message
  }
  return props.item.Message
})

const canCancel = computed(() => props.item.State === 'queued' || props.item.State === 'active')
const canRetry = computed(() => (props.item.State === 'failed' || props.item.State === 'cancelled') && props.item.Size > 0)
</script>

<template>
  <div class="group flex items-start gap-2 rounded-md border bg-card px-2.5 py-1.5">
    <span
      :class="['mt-0.5 shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium', stateConfig.color, stateConfig.bg]"
    >
      {{ stateConfig.label }}
    </span>
    <div class="min-w-0 flex-1">
      <p
        class="truncate text-xs"
        :title="item.Paths.join('\n')"
      >
        {{ item.Name }}
      </p>
      <p
        v-if="detail"
        :class="[
          'text-[10px] leading-snug break-words',
          item.State === 'failed' ? 'text-destructive' : 'text-muted-foreground'
        ]"
      >
        {{ detail }}
      </p>
    </div>
    <button
      v-if="canCancel"
      class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
      :title="item.State === 'active' ? 'Cancel this upload' : 'Remove from queue'"
      @click="$emit('cancel')"
    >
      <X :size="14" />
    </button>
    <button
      v-else-if="canRetry"
      class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
      title="Retry"
      @click="$emit('retry')"
    >
      <RotateCcw :size="14" />
    </button>
  </div>
</template>
