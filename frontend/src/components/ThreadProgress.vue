<script setup lang="ts">
import { computed, toRef } from 'vue'
import type { ThreadStatus } from '../utils/UploadManager'
import { useUploadSpeed } from '../utils/useUploadSpeed'
import { Progress } from '@/components/ui/progress'

const props = defineProps<{
  thread: ThreadStatus
}>()

const statusConfig = computed(() => {
  switch (props.thread.Status) {
    case 'error':
      return { color: 'text-destructive', bg: 'bg-destructive/10', label: 'Error' }
    case 'completed':
      return { color: 'text-primary', bg: 'bg-primary/10', label: 'Done' }
    case 'skipped':
      return { color: 'text-amber-700 dark:text-amber-300', bg: 'bg-amber-500/10', label: 'Skipped' }
    case 'uploading':
      if (props.thread.Attempt > 1) {
        return { color: 'text-orange-500', bg: 'bg-orange-500/10', label: `Retry ${props.thread.Attempt - 1}` }
      }
      return { color: 'text-blue-500', bg: 'bg-blue-500/10', label: 'Uploading' }
    case 'hashing':
      return { color: 'text-cyan-500', bg: 'bg-cyan-500/10', label: 'Hashing' }
    case 'checking':
      return { color: 'text-yellow-500', bg: 'bg-yellow-500/10', label: 'Checking' }
    case 'finalizing':
      return { color: 'text-purple-500', bg: 'bg-purple-500/10', label: 'Finalizing' }
    case 'idle':
      return { color: 'text-muted-foreground', bg: 'bg-muted', label: 'Waiting' }
    default:
      return { color: 'text-muted-foreground', bg: 'bg-muted', label: props.thread.Status }
  }
})

const uploadPercent = computed(() => {
  if (props.thread.Status !== 'uploading' || !props.thread.BytesTotal) {
    return null
  }
  return Math.round((props.thread.BytesUploaded / props.thread.BytesTotal) * 100)
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

const fileSizeDisplay = computed(() => {
  if (props.thread.Status !== 'uploading' || !props.thread.BytesTotal) {
    return null
  }
  return `${formatBytes(props.thread.BytesUploaded)}/${formatBytes(props.thread.BytesTotal)}`
})

const speed = useUploadSpeed(toRef(props, 'thread'))
const speedDisplay = computed(() => speed.value === null ? '' : `${formatBytes(Math.round(speed.value))}/s`)

const statusMessage = computed(() => props.thread.Message.replace(/^Error:\s*/, ''))
</script>

<template>
  <!-- Fixed height: the second line is always there, as a progress bar while
       bytes are sent and as one truncated status line otherwise, so rows do
       not change size as a file moves through its stages. -->
  <div class="h-[46px] px-2.5 py-1.5 bg-card border rounded-md flex flex-col justify-between overflow-hidden">
    <div class="flex items-center gap-2 min-w-0">
      <span
        :class="[
          'shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded',
          statusConfig.color,
          statusConfig.bg
        ]"
      >
        {{ statusConfig.label }}
      </span>
      <span
        class="flex-1 min-w-0 text-xs truncate"
        :class="thread.FileName ? '' : 'text-muted-foreground'"
        :title="thread.FilePath || thread.Message"
      >
        {{ thread.FileName || 'Waiting for the next file' }}
      </span>
      <span
        v-if="uploadPercent !== null"
        class="shrink-0 text-[10px] text-muted-foreground tabular-nums"
      >
        {{ fileSizeDisplay }} · {{ uploadPercent }}%<template v-if="speedDisplay"> · <span class="text-foreground">{{ speedDisplay }}</span></template>
      </span>
    </div>
    <Progress
      v-if="uploadPercent !== null"
      :model-value="uploadPercent"
      class="h-1"
    />
    <p
      v-else
      :class="[
        'text-[10px] leading-tight truncate',
        thread.Status === 'error' ? 'text-destructive' : 'text-muted-foreground'
      ]"
      :title="statusMessage"
    >
      {{ thread.Status === 'idle' ? '\u00a0' : statusMessage }}
    </p>
  </div>
</template>
