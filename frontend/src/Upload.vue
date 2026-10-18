<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import Button from "./components/ui/button/Button.vue"
import { Progress } from "./components/ui/progress"
import { ScrollArea } from "./components/ui/scroll-area"
import ThreadProgress from "./components/ThreadProgress.vue"
import QueueItemRow from "./components/QueueItemRow.vue"
import { uploadManager, type QueueItem, type QueueItemState } from './utils/UploadManager'
import { Sheet, SheetContent, SheetTrigger } from './components/ui/sheet'
import { TooltipProvider } from './components/ui/tooltip'
import SettingsPanel from './SettingsPanel.vue'
import { Settings, X, Clock, Zap, FolderPlus, AlertTriangle, Pause, Play, RotateCcw, Trash2, WifiOff, Copy, Hourglass, Upload as UploadIcon } from '@lucide/vue'

const { state } = uploadManager

// Elapsed time ticker
const elapsedSeconds = ref(0)
// Wall clock, refreshed by the ticker for countdowns.
const now = ref(Date.now())
let elapsedInterval: ReturnType<typeof setInterval> | null = null
// Speed smoothed over roughly the last ten seconds, so the ETA does not jump
// with every progress sample.
const smoothedSpeed = ref(0)

onMounted(() => {
  elapsedInterval = setInterval(() => {
    now.value = Date.now()
    if (state.startTime > 0 && state.isUploading) {
      elapsedSeconds.value = Math.floor((Date.now() - state.startTime) / 1000)
      smoothedSpeed.value = smoothedSpeed.value === 0
        ? state.uploadSpeed
        : smoothedSpeed.value * 0.8 + state.uploadSpeed * 0.2
    }
  }, 1000)
})

onUnmounted(() => {
  if (elapsedInterval) {
    clearInterval(elapsedInterval)
  }
})

// One slot per running worker, kept while it waits between files so the list
// does not grow and shrink with every item. Workers that finished drop out.
const threadsList = computed(() => {
  if (!state.isUploading) return []
  return Array.from(state.threads.values())
    .filter(thread => !(thread.Status === 'idle' && thread.Message === 'Finished'))
    .sort((a, b) => a.WorkerID - b.WorkerID)
})
// Rows still busy with a file, for matching queue items to their progress.
const busyThreads = computed(() => threadsList.value.filter(thread => thread.Status !== 'idle'))

type Filter = 'all' | QueueItemState

// Every list statistic in one pass: with tens of thousands of items, a pass
// per statistic made each update noticeably slow.
const stats = computed(() => {
  const counts: Record<Filter, number> = { all: state.items.length, queued: 0, active: 0, done: 0, failed: 0, skipped: 0, cancelled: 0 }
  let runTotal = 0
  let runFinished = 0
  let uploadableSkips = 0
  for (const item of state.items) {
    counts[item.State]++
    if (item.State === 'skipped' && item.SkipCode) uploadableSkips++
    if (item.runId === state.runId) {
      runTotal++
      if (item.State !== 'queued' && item.State !== 'active') runFinished++
    }
  }
  return { counts, runTotal, runFinished, uploadableSkips }
})
const counts = computed(() => stats.value.counts)
const runTotal = computed(() => stats.value.runTotal)
const runFinished = computed(() => stats.value.runFinished)
const uploadableSkips = computed(() => stats.value.uploadableSkips)

const progressPercent = computed(() => {
  if (runTotal.value === 0) return 0
  return Math.round((runFinished.value / runTotal.value) * 100)
})

const filter = ref<Filter>('all')
const filters: { value: Filter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'queued', label: 'Queued' },
  { value: 'active', label: 'Active' },
  { value: 'done', label: 'Done' },
  { value: 'failed', label: 'Failed' },
  { value: 'skipped', label: 'Skipped' },
  { value: 'cancelled', label: 'Cancelled' },
]


// Rendering thousands of rows at once stalls the webview; grow the list on demand.
const pageSize = 200
const visibleCount = ref(pageSize)
const filteredItems = computed<QueueItem[]>(() => {
  if (filter.value === 'all') return state.items
  return state.items.filter(item => item.State === filter.value)
})
const visibleItems = computed(() => filteredItems.value.slice(0, visibleCount.value))

function setFilter(value: Filter) {
  filter.value = value
  visibleCount.value = pageSize
}

const activeThreadFor = (item: QueueItem) => busyThreads.value.find(thread => thread.FilePath === item.Path)

// Format bytes to human readable
function formatBytes(bytes: number, decimals = 1): string {
  if (bytes <= 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1)
  return parseFloat((bytes / Math.pow(k, i)).toFixed(decimals)) + ' ' + sizes[i]
}

const speedDisplay = computed(() => {
  if (!state.isUploading || state.uploadSpeed <= 0) return '--'
  return formatBytes(state.uploadSpeed) + '/s'
})

function formatDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.round(totalSeconds))
  if (seconds < 60) return `${seconds}s`
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  if (mins < 60) return `${mins}m ${secs}s`
  const hours = Math.floor(mins / 60)
  const remainingMins = mins % 60
  return `${hours}h ${remainingMins}m`
}

const elapsedDisplay = computed(() => formatDuration(elapsedSeconds.value))

// Time left for the queued and active items: remaining bytes over the smoothed
// speed, or, before any bytes move, the average time per finished item.
const etaDisplay = computed(() => {
  if (!state.isUploading || state.queueStatus.Paused || state.queueStatus.WaitingForNetwork || state.queueStatus.RateLimitedUntil > 0) return '--'
  const remainingItems = runTotal.value - runFinished.value
  if (remainingItems <= 0) return '--'
  const remainingBytes = state.totalBytes - state.uploadedBytes
  if (remainingBytes > 0 && smoothedSpeed.value > 0) {
    return formatDuration(remainingBytes / smoothedSpeed.value)
  }
  if (runFinished.value > 0 && elapsedSeconds.value > 0) {
    return formatDuration((elapsedSeconds.value / runFinished.value) * remainingItems)
  }
  return '--'
})

const bytesDisplay = computed(() => {
  if (state.totalBytes === 0) return ''
  return `${formatBytes(state.uploadedBytes)} / ${formatBytes(state.totalBytes)}`
})

const albumProgressPercent = computed(() => {
  if (!state.albumStatus || state.albumStatus.TotalItems === 0) return 0
  return Math.round((state.albumStatus.ItemsAdded / state.albumStatus.TotalItems) * 100)
})

const hasPendingUploads = computed(() => counts.value.queued > 0 || counts.value.active > 0)

const scanLabel = computed(() => {
  const scan = state.queueStatus.Scan
  if (!scan?.Phase) return ''
  if (scan.Phase === 'checking') {
    return `Checking Live Photos: ${scan.Checked.toLocaleString()} of ${scan.Total.toLocaleString()} files`
  }
  return `Scanning: ${scan.Found.toLocaleString()} files found`
})
const scanPercent = computed(() => {
  const scan = state.queueStatus.Scan
  return scan?.Phase === 'checking' && scan.Total > 0 ? Math.round((scan.Checked / scan.Total) * 100) : null
})

// One row per kind of warning, with its files, instead of one row each.
const warningGroups = computed(() => {
  const groups = new Map<string, { message: string; paths: string[] }>()
  for (const warning of state.warnings) {
    const key = `${warning.Code}\u0000${warning.Message}`
    const group = groups.get(key) ?? { message: warning.Message, paths: [] }
    group.paths.push(...warning.Paths)
    groups.set(key, group)
  }
  return [...groups.values()]
})

// Seconds until a rate-limit pause ends.
const rateLimitSeconds = computed(() => {
  const until = state.queueStatus.RateLimitedUntil
  return until > 0 ? Math.max(0, Math.ceil((until - now.value) / 1000)) : 0
})

const statusLabel = computed(() => {
  if (!state.isUploading) return 'Finished'
  if (state.queueStatus.StoppedReason) return 'Stopped'
  if (state.queueStatus.WaitingForNetwork) return 'Waiting for connection'
  if (state.queueStatus.RateLimitedUntil > 0) return `Rate limited by Google, resuming in ${formatDuration(rateLimitSeconds.value)}`
  if (state.queueStatus.Paused) return counts.value.active > 0 ? 'Pausing after current items' : 'Paused'
  if (!hasPendingUploads.value && state.isCreatingAlbum) return 'Adding to album'
  if (!hasPendingUploads.value && scanLabel.value) return 'Scanning'
  if (!hasPendingUploads.value) return 'Finishing'
  return 'Uploading'
})

const showWarnings = ref(false)

function warningFiles(paths: string[]): string {
  return paths.map(path => path.split(/[\\/]/).pop()).filter(Boolean).join(', ')
}

const copyLabel = ref('Copy JSON')
async function copyResults() {
  const copied = await uploadManager.copyResultsAsJson()
  copyLabel.value = copied ? 'Copied!' : 'Copy failed'
  setTimeout(() => copyLabel.value = 'Copy JSON', 1000)
}
</script>

<template>
  <div
    class="flex flex-col h-full w-full px-4 pt-8 pb-4 gap-3"
    style="--wails-draggable: none"
  >
    <!-- Header -->
    <div class="relative text-center">
      <Sheet>
        <SheetTrigger as-child>
          <button
            class="absolute right-0 top-0 rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
            title="Settings"
          >
            <Settings :size="16" />
          </button>
        </SheetTrigger>
        <SheetContent side="bottom">
          <p class="px-4 pt-4 text-xs text-muted-foreground">
            Changes apply to files you add from now on. Items already in the queue keep the settings they were added with.
          </p>
          <TooltipProvider disable-hoverable-content>
            <SettingsPanel />
          </TooltipProvider>
        </SheetContent>
      </Sheet>
      <p class="text-2xl font-bold tabular-nums">
        {{ runFinished }}<span class="text-muted-foreground font-normal">/</span><span class="text-muted-foreground">{{ runTotal }}</span>
      </p>
      <p class="text-xs text-muted-foreground flex items-center justify-center gap-1.5">
        <WifiOff
          v-if="state.queueStatus.WaitingForNetwork"
          :size="12"
        />
        {{ statusLabel }}
      </p>
    </div>

    <!-- Scan progress for drops still being read -->
    <div
      v-if="scanLabel"
      class="rounded-md border bg-muted/30 px-2.5 py-1.5"
    >
      <p class="text-xs text-muted-foreground tabular-nums">
        {{ scanLabel }}
      </p>
      <Progress
        v-if="scanPercent !== null"
        :model-value="scanPercent"
        class="h-1 mt-1"
      />
    </div>

    <div class="flex gap-3 justify-center text-xs text-muted-foreground">
      <div class="flex items-center gap-1.5">
        <Zap :size="12" />
        <span class="tabular-nums text-foreground">{{ speedDisplay }}</span>
      </div>
      <span class="text-border">|</span>
      <div class="flex items-center gap-1.5">
        <Clock :size="12" />
        <span class="tabular-nums text-foreground">{{ elapsedDisplay }}</span>
      </div>
      <span class="text-border">|</span>
      <div
        class="flex items-center gap-1.5"
        title="Estimated time left for the queued items"
      >
        <Hourglass :size="12" />
        <span class="tabular-nums text-foreground">{{ etaDisplay }}</span>
        <span>left</span>
      </div>
    </div>

    <div>
      <Progress
        :model-value="progressPercent"
        class="h-2.5"
      />
      <div class="flex justify-between mt-1.5 text-xs text-muted-foreground">
        <span v-if="bytesDisplay">{{ bytesDisplay }}</span>
        <span v-else>&nbsp;</span>
        <span class="font-medium">{{ progressPercent }}%</span>
      </div>
    </div>

    <!-- Queue controls -->
    <div class="flex gap-2">
      <template v-if="state.isUploading">
        <Button
          variant="outline"
          size="sm"
          class="flex-1 cursor-pointer"
          @click="uploadManager.setPaused(!state.queueStatus.Paused)"
        >
          <Play
            v-if="state.queueStatus.Paused"
            :size="14"
          />
          <Pause
            v-else
            :size="14"
          />
          {{ state.queueStatus.Paused ? 'Resume' : 'Pause' }}
        </Button>
        <Button
          variant="destructive"
          size="sm"
          class="flex-1 cursor-pointer"
          @click="uploadManager.cancelUpload()"
        >
          <X :size="14" />
          {{ hasPendingUploads ? 'Cancel all' : 'Stop album' }}
        </Button>
      </template>
      <template v-else>
        <Button
          v-if="counts.failed > 0"
          variant="outline"
          size="sm"
          class="flex-1 cursor-pointer"
          @click="uploadManager.retryFailed()"
        >
          <RotateCcw :size="14" />
          Retry failed ({{ counts.failed }})
        </Button>
        <Button
          v-if="uploadableSkips > 0"
          variant="outline"
          size="sm"
          class="flex-1 cursor-pointer"
          title="Upload files skipped as incomplete Live Photos as ordinary photos and videos"
          @click="uploadManager.uploadSkipped()"
        >
          <UploadIcon :size="14" />
          Upload skipped ({{ uploadableSkips }})
        </Button>
        <Button
          variant="outline"
          size="sm"
          class="flex-1 cursor-pointer"
          @click="copyResults"
        >
          <Copy :size="14" />
          {{ copyLabel }}
        </Button>
        <Button
          size="sm"
          class="flex-1 cursor-pointer"
          @click="uploadManager.clearFinished()"
        >
          <Trash2 :size="14" />
          Clear list
        </Button>
      </template>
    </div>

    <div
      v-if="state.isUploading && state.queueStatus.StoppedReason"
      class="flex gap-2 rounded-md bg-destructive/10 px-2.5 py-2 text-xs text-destructive"
    >
      <AlertTriangle
        :size="14"
        class="mt-0.5 shrink-0"
      />
      <p class="leading-snug">
        {{ state.queueStatus.StoppedReason }}
      </p>
    </div>

    <!-- Album creation progress -->
    <div
      v-if="state.albumStatus && (state.isCreatingAlbum || state.albumStatus.IsComplete)"
      class="p-3 rounded-lg border bg-muted/30"
    >
      <div class="flex items-center gap-2 mb-2">
        <FolderPlus
          :size="14"
          class="text-primary"
        />
        <span class="text-sm font-medium">
          {{ state.isCreatingAlbum ? 'Adding to album...' : 'Added to album' }}
        </span>
        <span class="ml-auto text-xs text-muted-foreground truncate">{{ state.albumStatus.AlbumName }}</span>
      </div>
      <Progress
        :model-value="albumProgressPercent"
        class="h-1.5"
      />
      <p class="text-xs text-muted-foreground mt-1">
        {{ state.albumStatus.ItemsAdded }} / {{ state.albumStatus.TotalItems }} items
      </p>
    </div>

    <!-- Warnings, collapsed by default -->
    <div v-if="state.warnings.length">
      <button
        class="flex w-full items-center gap-2 rounded-md bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-700 dark:text-amber-300 cursor-pointer"
        @click="showWarnings = !showWarnings"
      >
        <AlertTriangle :size="14" />
        {{ warningGroups.length }} kind(s) of warning, {{ state.warnings.length }} total
        <span class="ml-auto opacity-75">{{ showWarnings ? 'Hide' : 'Show' }}</span>
      </button>
      <div
        v-if="showWarnings"
        class="mt-1.5 max-h-32 overflow-y-auto space-y-1.5"
      >
        <div
          v-for="(group, index) in warningGroups"
          :key="index"
          class="rounded-md bg-amber-500/10 px-2.5 py-1.5 text-amber-700 dark:text-amber-300"
        >
          <p class="text-xs leading-snug">
            {{ group.message }} ({{ group.paths.length }} file(s))
          </p>
          <p class="text-[10px] opacity-75 break-words">
            {{ warningFiles(group.paths.slice(0, 12)) }}{{ group.paths.length > 12 ? `, and ${group.paths.length - 12} more` : '' }}
          </p>
        </div>
      </div>
    </div>

    <!-- Active uploads with live progress -->
    <div
      v-if="threadsList.length"
      class="space-y-1.5 max-h-48 overflow-y-auto"
    >
      <ThreadProgress
        v-for="thread in threadsList"
        :key="thread.WorkerID"
        :thread="thread"
      />
    </div>

    <!-- Filters -->
    <div class="flex flex-wrap gap-1">
      <button
        v-for="option in filters"
        :key="option.value"
        :class="[
          'rounded-full px-2 py-0.5 text-[11px] border cursor-pointer tabular-nums',
          filter === option.value ? 'bg-primary text-primary-foreground border-primary' : 'text-muted-foreground hover:bg-muted'
        ]"
        @click="setFilter(option.value)"
      >
        {{ option.label }} {{ counts[option.value] }}
      </button>
    </div>

    <!-- Item list -->
    <div class="flex-1 min-h-0">
      <ScrollArea class="h-full">
        <div class="space-y-1 pr-3">
          <QueueItemRow
            v-for="item in visibleItems"
            :key="item.ID"
            :item="item"
            :thread="item.State === 'active' ? activeThreadFor(item) : undefined"
            @cancel="uploadManager.cancelItem(item.ID)"
            @retry="uploadManager.retryItem(item.ID)"
          />
          <p
            v-if="filteredItems.length === 0"
            class="py-6 text-center text-xs text-muted-foreground"
          >
            {{ state.isUploading ? 'Scanning files...' : 'No items' }}
          </p>
          <Button
            v-if="filteredItems.length > visibleCount"
            variant="ghost"
            size="sm"
            class="w-full cursor-pointer"
            @click="visibleCount += pageSize"
          >
            Show more ({{ filteredItems.length - visibleCount }} hidden)
          </Button>
        </div>
      </ScrollArea>
    </div>

    <p class="text-center text-[11px] text-muted-foreground select-none">
      Drop more files here to add them to the queue
    </p>
  </div>
</template>
