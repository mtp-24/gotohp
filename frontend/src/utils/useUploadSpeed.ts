import { computed, onMounted, onUnmounted, ref, watch, type Ref } from 'vue'
import type { ThreadStatus } from './UploadManager'

// How far back the speed looks: long enough to smooth out bursts, short
// enough to show a connection slowing down within a few seconds.
const windowMs = 4000

// useUploadSpeed measures one upload's speed from its progress updates, in
// bytes per second, or null when the thread is not sending bytes. It starts
// over for a new file or a retry, and drops to 0 when progress stops.
export function useUploadSpeed(thread: Ref<ThreadStatus | undefined>) {
  const samples: { t: number; bytes: number }[] = []
  let key = ''
  const now = ref(Date.now())
  let timer: ReturnType<typeof setInterval> | null = null
  onMounted(() => { timer = setInterval(() => { now.value = Date.now() }, 1000) })
  onUnmounted(() => { if (timer) clearInterval(timer) })

  watch(() => thread.value?.BytesUploaded, () => {
    const t = thread.value
    if (!t || t.Status !== 'uploading') {
      samples.length = 0
      key = ''
      return
    }
    const next = `${t.FilePath}\u0000${t.Attempt}`
    if (next !== key || (samples.length && t.BytesUploaded < samples[samples.length - 1].bytes)) {
      samples.length = 0
      key = next
    }
    const time = Date.now()
    samples.push({ t: time, bytes: t.BytesUploaded })
    while (samples.length > 2 && time - samples[0].t > windowMs) samples.shift()
    now.value = time
  }, { immediate: true })

  return computed(() => {
    const t = thread.value
    if (!t || t.Status !== 'uploading' || samples.length === 0) return null
    const last = samples[samples.length - 1]
    if (t.BytesTotal && last.bytes >= t.BytesTotal) return null // waiting on Google
    // Counted up to now, so a stalled upload falls towards 0.
    const first = samples[0]
    const span = Math.max(now.value, last.t) - first.t
    if (span < 1000) return null
    return Math.max(0, (last.bytes - first.bytes) / (span / 1000))
  })
}
