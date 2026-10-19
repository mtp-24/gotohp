<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import Button from "./components/ui/button/Button.vue"
import { Progress } from "./components/ui/progress"
import { ScrollArea } from "./components/ui/scroll-area"
import ThreadProgress from "./components/ThreadProgress.vue"
import QueueItemRow from "./components/QueueItemRow.vue"
import { uploadManager, type QueueItem, type QueueItemState } from './utils/UploadManager'
import { newestFirst } from './utils/newestFirst'
import { Sheet, SheetContent, SheetTrigger } from './components/ui/sheet'
import { TooltipProvider } from './components/ui/tooltip'
import SettingsPanel from './SettingsPanel.vue'
import { Settings, X, Clock, Zap, AlertTriangle, Pause, Play, RotateCcw, Trash2, WifiOff, Copy, Hourglass, Upload as UploadIcon, Loader2, CheckCircle2, Check } from '@lucide/vue'

const { state } = uploadManager

// Elapsed time ticker
const elapsedSeconds = ref(0)
// Wall clock, refreshed by the ticker for countdowns.
const now = ref(Date.now())
let elapsedInterval: ReturnType<typeof setInterval> | null = null
// Samples of finished work over the last few minutes, for the ETA.
const workSamples: { t: number; work: number }[] = []
const etaWindowMs = 2 * 60 * 1000
const etaMinSampleMs = 20 * 1000
let sampledRunId = -1

onMounted(() => {
  elapsedInterval = setInterval(() => {
    now.value = Date.now()
    if (state.startTime > 0 && state.isUploading) {
      elapsedSeconds.value = Math.floor((Date.now() - state.startTime) / 1000)
      sampleWork()
    }
  }, 1000)
})

onUnmounted(() => {
  if (elapsedInterval) {
    clearInterval(elapsedInterval)
  }
  if (scanHideTimer) clearTimeout(scanHideTimer)
  if (dropBadgeTimer) clearTimeout(dropBadgeTimer)
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
  // The list is not reactive; listVersion marks when it changed.
  void state.listVersion
  const counts: Record<Filter, number> = { all: state.items.length, queued: 0, active: 0, done: 0, failed: 0, skipped: 0, cancelled: 0 }
  let runTotal = 0
  let runFinished = 0
  let runCancelled = 0
  let uploadableSkips = 0
  // Work in this run, measured in bytes plus a fixed cost per file.
  let runWork = 0
  let runWorkFinished = 0
  for (const item of state.items) {
    counts[item.State]++
    if (item.State === 'skipped' && item.SkipCode) uploadableSkips++
    if (item.runId === state.runId) {
      runTotal++
      const work = itemWork(item)
      runWork += work
      if (item.State !== 'queued' && item.State !== 'active') {
        runFinished++
        runWorkFinished += work
        if (item.State === 'cancelled') runCancelled++
      }
    }
  }
  return { counts, runTotal, runFinished, runCancelled, uploadableSkips, runWork, runWorkFinished }
})
const counts = computed(() => stats.value.counts)
const runTotal = computed(() => stats.value.runTotal)
const runFinished = computed(() => stats.value.runFinished)
// Processed items went through the queue: uploaded, skipped or failed.
// Cancelled items were never processed, so they are counted apart.
const runCancelled = computed(() => stats.value.runCancelled)
const runProcessed = computed(() => runFinished.value - runCancelled.value)
const uploadableSkips = computed(() => stats.value.uploadableSkips)

const progressPercent = computed(() => {
  if (runTotal.value === 0) return 0
  return Math.round((runProcessed.value / runTotal.value) * 100)
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
  void state.listVersion
  if (filter.value === 'all') return state.items
  return state.items.filter(item => item.State === filter.value)
})
// Queued and Active are in upload order, so the next file is on top and
// files leave as they move on. The other tabs grow as files finish, so they
// show the most recently changed first: new results appear on top. All is an
// overview in sections instead of one list (see overview below).
const uploadOrderFilters: Filter[] = ['queued', 'active']
const visibleItems = computed(() => {
  void state.listVersion
  if (filter.value === 'all') return []
  const limit = visibleCount.value
  if (uploadOrderFilters.includes(filter.value)) {
    return filteredItems.value.slice(0, limit)
  }
  return newestFirst(filteredItems.value, limit)
})

// The All tab shows a few rows of each kind, each section linking to its full
// tab: what needs attention, what is next and the latest results. It is at
// most a few dozen rows however long the queue is.
const overviewFailed = 5
const overviewNext = 5
const overviewFinished = 20
const overview = computed(() => {
  void state.listVersion
  if (filter.value !== 'all') return null
  const failed: QueueItem[] = []
  const next: QueueItem[] = []
  const finished: QueueItem[] = []
  for (const item of state.items) {
    if (item.State === 'failed') failed.push(item)
    else if (item.State === 'done' || item.State === 'skipped') finished.push(item)
    // The list is in upload order, so the first queued items go next.
    else if (item.State === 'queued' && next.length < overviewNext) next.push(item)
  }
  return {
    failed: newestFirst(failed, overviewFailed),
    next,
    finished: newestFirst(finished, overviewFinished),
    finishedCount: finished.length,
  }
})

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

// The ETA projects how fast the queue has really been finishing work over
// the last two minutes onto the work left. Work counts every file's bytes
// plus a fixed cost per file, whatever happens to it: a file already in the
// library is hashed and skipped quickly without sending its bytes, so an
// estimate from upload speed alone wildly overstates the time left.
const perFileWork = 2 * 1024 * 1024
function itemWork(item: QueueItem): number {
  return Math.max(0, item.Size) + perFileWork
}

const etaHeld = computed(() => state.queueStatus.Paused || state.queueStatus.WaitingForNetwork || state.queueStatus.RateLimitedUntil > 0)
const etaSeconds = ref<number | null>(null)

function sampleWork() {
  const nowMs = Date.now()
  if (sampledRunId !== state.runId || etaHeld.value) {
    // A new run, or a pause that would read as slowness: start measuring again.
    sampledRunId = state.runId
    workSamples.length = 0
    etaSeconds.value = null
    if (etaHeld.value) return
  }
  // Bytes already sent by files still uploading count as finished work.
  const inFlight = busyThreads.value.reduce((sum, thread) => sum + (thread.Status === 'uploading' ? thread.BytesUploaded : 0), 0)
  const work = stats.value.runWorkFinished + inFlight
  workSamples.push({ t: nowMs, work })
  while (workSamples.length > 2 && nowMs - workSamples[0].t > etaWindowMs) workSamples.shift()

  const first = workSamples[0]
  const spanMs = nowMs - first.t
  const done = work - first.work
  const remaining = stats.value.runWork - work
  if (spanMs < etaMinSampleMs || done <= 0 || remaining <= 0) {
    if (remaining <= 0) etaSeconds.value = null
    return
  }
  etaSeconds.value = remaining / (done / (spanMs / 1000))
}

// Rounded so the estimate reads as one, and does not flicker by the second.
function formatEta(seconds: number): string {
  const minutes = Math.round(seconds / 60)
  if (minutes < 1) return 'under a minute'
  if (minutes < 60) return `about ${minutes} min`
  const roundedMinutes = Math.round(seconds / 300) * 5
  const hours = Math.floor(roundedMinutes / 60)
  const rest = roundedMinutes % 60
  if (hours < 24) return rest ? `about ${hours} h ${rest} min` : `about ${hours} h`
  const days = Math.floor(hours / 24)
  return `about ${days} d ${hours % 24} h`
}

const etaDisplay = computed(() => {
  if (!state.isUploading || etaHeld.value || runFinished.value >= runTotal.value) return '--'
  if (etaSeconds.value === null) return 'estimating…'
  return `${formatEta(etaSeconds.value)} left`
})

// Cancelled files leave the byte total, so once the run is over only the
// uploaded amount is shown, not a misleading "x of x".
const bytesDisplay = computed(() => {
  if (state.totalBytes === 0) return ''
  if (!state.isUploading) return `${formatBytes(state.uploadedBytes)} uploaded`
  return `${formatBytes(state.uploadedBytes)} / ${formatBytes(state.totalBytes)}`
})

const albumsBusy = computed(() => state.albums.filter(album => album.state === 'waiting' || album.state === 'adding').length)
const albumsDone = computed(() => state.albums.filter(album => album.state === 'done').length)
const albumsFailed = computed(() => state.albums.filter(album => album.state === 'failed').length)
const albumTitle = computed(() => {
  const count = state.albums.length
  if (albumsBusy.value) return `Adding to albums… ${albumsDone.value} of ${count} done`
  if (albumsFailed.value) return `${albumsDone.value} album(s) updated, ${albumsFailed.value} failed`
  return count === 1 ? 'Album updated' : `All ${count} albums updated`
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

// A quick scan would flash past unseen, so the scan line stays up for at
// least a moment, ending on "Scan finished".
const minScanDisplayMs = 1500
const shownScanLabel = ref('')
let scanShownAt = 0
let scanHideTimer: ReturnType<typeof setTimeout> | null = null
watch(scanLabel, (label) => {
  if (scanHideTimer) {
    clearTimeout(scanHideTimer)
    scanHideTimer = null
  }
  if (label) {
    if (!shownScanLabel.value) scanShownAt = Date.now()
    shownScanLabel.value = label
    return
  }
  if (!shownScanLabel.value) return
  shownScanLabel.value = 'Scan finished'
  scanHideTimer = setTimeout(() => {
    shownScanLabel.value = ''
    scanHideTimer = null
  }, Math.max(800, minScanDisplayMs - (Date.now() - scanShownAt)))
}, { immediate: true })

// Files added to a run that is already going change the total, the bytes and
// the percentage at once; say what was added, and mark it next to the numbers.
const dropBadge = ref<{ added: number; bytes: number } | null>(null)
let dropBadgeTimer: ReturnType<typeof setTimeout> | null = null
watch(() => state.lastDrop, (drop) => {
  if (!drop || !drop.midRun) return
  const notes: string[] = []
  if (drop.alreadyQueued) notes.push(`${drop.alreadyQueued.toLocaleString()} already in the queue`)
  if (drop.skipped) notes.push(`${drop.skipped.toLocaleString()} skipped`)
  if (drop.added) {
    toast.success(`Added ${drop.added.toLocaleString()} file(s) to the queue`, {
      description: [`${formatBytes(drop.bytes)} more to upload`, ...notes].join(' · '),
    })
    dropBadge.value = { added: drop.added, bytes: drop.bytes }
    if (dropBadgeTimer) clearTimeout(dropBadgeTimer)
    dropBadgeTimer = setTimeout(() => { dropBadge.value = null }, 6000)
  } else {
    toast.info('No new files added', { description: notes.join(' · ') || 'The drop had no files to upload.' })
  }
})

// The queue saved before the app last quit comes back paused; say so.
watch(() => state.restored, (restored) => {
  if (!restored) return
  const parts = [`${restored.Finished.toLocaleString()} finished`]
  if (restored.Remaining) parts.push(`${restored.Remaining.toLocaleString()} to upload`)
  if (restored.Failed) parts.push(`${restored.Failed.toLocaleString()} failed`)
  if (restored.AlbumFiles) parts.push(`${restored.AlbumFiles.toLocaleString()} to add to albums`)
  toast.success('Restored your queue from last time', {
    description: parts.join(' · ') + (restored.Remaining ? '. Press Resume to carry on.' : ''),
    duration: 10000,
  })
  // Once only, even if this screen is shown again later.
  state.restored = null
}, { immediate: true })

// The library listing large drops are checked against, and what it saved.
const libraryLabel = computed(() => {
  const library = state.queueStatus.Library
  if (!state.isUploading || !library?.Phase) return ''
  if (library.Phase === 'listing') return 'Reading your Google Photos library to check files against it…'
  if (library.Phase === 'failed') return 'Could not read your library; checking each file with Google instead'
  return `Library read: ${library.Items.toLocaleString()} items in ${library.Requests.toLocaleString()} requests · ` +
    `${library.Matched.toLocaleString()} files found in it · ${library.Lookups.toLocaleString()} checked with Google`
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
  if (!state.isUploading) return runCancelled.value > 0 ? 'Stopped' : 'Finished'
  if (state.queueStatus.StoppedReason) return 'Stopped'
  if (state.queueStatus.WaitingForNetwork) return 'Waiting for connection'
  if (state.queueStatus.RateLimitedUntil > 0) return `Rate limited by Google, resuming in ${formatDuration(rateLimitSeconds.value)}`
  if (state.queueStatus.Paused) return counts.value.active > 0 ? 'Pausing after current items' : 'Paused'
  if (!hasPendingUploads.value && state.isCreatingAlbum) return 'Adding to albums'
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
            Upload threads apply right away. Other changes apply to files you add from now on; items already in the queue keep the settings they were added with.
          </p>
          <TooltipProvider disable-hoverable-content>
            <SettingsPanel />
          </TooltipProvider>
        </SheetContent>
      </Sheet>
      <p class="text-2xl font-bold tabular-nums">
        {{ runProcessed.toLocaleString() }}<span class="text-muted-foreground font-normal">/</span><span class="text-muted-foreground">{{ runTotal.toLocaleString() }}</span><span
          v-if="dropBadge"
          class="ml-1.5 align-middle text-sm font-semibold text-primary"
        >+{{ dropBadge.added.toLocaleString() }}</span>
      </p>
      <p class="text-xs text-muted-foreground flex items-center justify-center gap-1.5">
        <WifiOff
          v-if="state.queueStatus.WaitingForNetwork"
          :size="12"
        />
        {{ statusLabel }}<template v-if="runCancelled">
          · {{ runCancelled.toLocaleString() }} cancelled
        </template>
      </p>
    </div>

    <!-- Scan progress for drops still being read -->
    <div
      v-if="shownScanLabel"
      class="rounded-md border bg-muted/30 px-2.5 py-1.5"
    >
      <p class="text-xs text-muted-foreground tabular-nums">
        {{ shownScanLabel }}
      </p>
      <Progress
        v-if="scanPercent !== null"
        :model-value="scanPercent"
        class="h-1 mt-1"
      />
    </div>

    <p
      v-if="libraryLabel"
      class="rounded-md border bg-muted/30 px-2.5 py-1.5 text-[11px] leading-snug text-muted-foreground tabular-nums"
      title="Files found in the listing are not looked up one by one. Files it does not have are still checked with Google before any upload."
    >
      {{ libraryLabel }}
    </p>

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
      </div>
    </div>

    <div>
      <Progress
        :model-value="progressPercent"
        class="h-2.5"
      />
      <div class="flex justify-between mt-1.5 text-xs text-muted-foreground">
        <span v-if="bytesDisplay">{{ bytesDisplay }}<span
          v-if="dropBadge && dropBadge.bytes"
          class="ml-1 font-semibold text-primary"
        >+{{ formatBytes(dropBadge.bytes) }}</span></span>
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

    <!-- Albums this run adds to -->
    <div
      v-if="state.albums.length"
      class="p-3 rounded-lg border bg-muted/30"
    >
      <div class="flex items-center gap-2">
        <Loader2
          v-if="albumsBusy"
          :size="14"
          class="text-primary animate-spin"
        />
        <AlertTriangle
          v-else-if="albumsFailed"
          :size="14"
          class="text-destructive"
        />
        <CheckCircle2
          v-else
          :size="14"
          class="text-primary"
        />
        <span class="text-sm font-medium">{{ albumTitle }}</span>
      </div>
      <ul class="mt-2 max-h-32 overflow-y-auto space-y-1">
        <li
          v-for="album in state.albums"
          :key="album.name"
          class="flex items-center gap-2 text-xs"
          :title="album.error || undefined"
        >
          <span class="min-w-0 flex-1 truncate">{{ album.name }}</span>
          <span
            :class="[
              'shrink-0 tabular-nums',
              album.state === 'failed' ? 'text-destructive' : 'text-muted-foreground'
            ]"
          >
            <template v-if="album.state === 'waiting'">waiting</template>
            <template v-else-if="album.state === 'failed'">failed</template>
            <template v-else>{{ album.added.toLocaleString() }} / {{ album.total.toLocaleString() }}</template>
          </span>
          <Check
            v-if="album.state === 'done'"
            :size="12"
            class="shrink-0 text-primary"
          />
        </li>
      </ul>
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

    <!-- Active uploads with live progress; the All tab shows them in its list. -->
    <div
      v-if="threadsList.length && filter !== 'all'"
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
        <div
          v-if="overview"
          class="space-y-1 pr-3"
        >
          <template v-if="threadsList.length">
            <div class="flex items-baseline gap-1.5 px-0.5 pt-1">
              <h3 class="text-[11px] font-semibold uppercase tracking-wide">
                Uploading now
              </h3>
              <span class="text-[11px] text-muted-foreground tabular-nums">{{ counts.active.toLocaleString() }}</span>
              <button
                class="ml-auto text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
                @click="setFilter('active')"
              >
                Active ›
              </button>
            </div>
            <ThreadProgress
              v-for="thread in threadsList"
              :key="thread.WorkerID"
              :thread="thread"
            />
          </template>

          <template v-if="overview.failed.length">
            <div class="flex items-baseline gap-1.5 px-0.5 pt-2">
              <h3 class="text-[11px] font-semibold uppercase tracking-wide text-destructive">
                Needs attention
              </h3>
              <span class="text-[11px] text-muted-foreground tabular-nums">{{ counts.failed.toLocaleString() }}</span>
              <button
                class="ml-auto flex items-center gap-1 text-[11px] font-medium hover:underline cursor-pointer"
                @click="uploadManager.retryFailed()"
              >
                <RotateCcw :size="11" />
                Retry all
              </button>
            </div>
            <QueueItemRow
              v-for="item in overview.failed"
              :key="item.ID"
              :item="item"
              @retry="uploadManager.retryItem(item.ID)"
            />
            <button
              v-if="counts.failed > overview.failed.length"
              class="w-full py-0.5 text-center text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
              @click="setFilter('failed')"
            >
              <span class="font-medium text-foreground">+{{ (counts.failed - overview.failed.length).toLocaleString() }} more</span> in Failed
            </button>
          </template>

          <template v-if="overview.next.length">
            <div class="flex items-baseline gap-1.5 px-0.5 pt-2">
              <h3 class="text-[11px] font-semibold uppercase tracking-wide">
                Up next
              </h3>
              <span class="text-[11px] text-muted-foreground tabular-nums">{{ counts.queued.toLocaleString() }}</span>
              <button
                class="ml-auto text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
                @click="setFilter('queued')"
              >
                Queued ›
              </button>
            </div>
            <QueueItemRow
              v-for="item in overview.next"
              :key="item.ID"
              :item="item"
              @cancel="uploadManager.cancelItem(item.ID)"
            />
            <button
              v-if="counts.queued > overview.next.length"
              class="w-full py-0.5 text-center text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
              @click="setFilter('queued')"
            >
              <span class="font-medium text-foreground">+{{ (counts.queued - overview.next.length).toLocaleString() }} more</span> in Queued
            </button>
          </template>

          <template v-if="overview.finished.length">
            <div class="flex items-baseline gap-1.5 px-0.5 pt-2">
              <h3 class="text-[11px] font-semibold uppercase tracking-wide">
                Recently finished
              </h3>
              <span class="text-[11px] text-muted-foreground tabular-nums">{{ overview.finishedCount.toLocaleString() }}</span>
              <button
                class="ml-auto text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
                @click="setFilter(counts.done ? 'done' : 'skipped')"
              >
                {{ counts.done ? 'Done' : 'Skipped' }} ›
              </button>
            </div>
            <QueueItemRow
              v-for="item in overview.finished"
              :key="item.ID"
              :item="item"
            />
            <button
              v-if="overview.finishedCount > overview.finished.length"
              class="w-full py-0.5 text-center text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
              @click="setFilter(counts.done ? 'done' : 'skipped')"
            >
              Older results are in Done<template v-if="counts.skipped">
                and Skipped
              </template>
            </button>
          </template>

          <button
            v-if="counts.cancelled"
            class="w-full py-1 text-center text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
            @click="setFilter('cancelled')"
          >
            {{ counts.cancelled.toLocaleString() }} cancelled ›
          </button>
          <p
            v-if="state.items.length === 0 && !threadsList.length"
            class="py-6 text-center text-xs text-muted-foreground"
          >
            {{ state.isUploading ? 'Scanning files...' : 'No items' }}
          </p>
        </div>
        <div
          v-else
          class="space-y-1 pr-3"
        >
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
