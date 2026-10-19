// newestFirst returns the k items with the highest `seq`, highest first,
// without sorting the whole list: a quickselect finds the cut-off in linear
// time, and only the items above it are sorted. With tens of thousands of
// queue items this keeps every list update cheap.
export function newestFirst<T extends { seq: number }>(items: readonly T[], k: number): T[] {
  if (k <= 0 || items.length === 0) return []
  if (items.length <= k) {
    return [...items].sort((a, b) => b.seq - a.seq)
  }
  const seqs = new Float64Array(items.length)
  for (let i = 0; i < items.length; i++) seqs[i] = items[i].seq
  const cutoff = kthLargest(seqs, k)
  const top: T[] = []
  for (const item of items) {
    if (item.seq >= cutoff) top.push(item)
  }
  return top.sort((a, b) => b.seq - a.seq).slice(0, k)
}

// kthLargest returns the k-th largest value (1-based) of values, reordering
// them in place.
function kthLargest(values: Float64Array, k: number): number {
  let lo = 0
  let hi = values.length - 1
  const target = k - 1
  while (lo < hi) {
    const pivot = values[(lo + hi) >>> 1]
    let i = lo
    let j = hi
    while (i <= j) {
      while (values[i] > pivot) i++
      while (values[j] < pivot) j--
      if (i <= j) {
        const t = values[i]
        values[i] = values[j]
        values[j] = t
        i++
        j--
      }
    }
    if (target <= j) hi = j
    else if (target >= i) lo = i
    else break
  }
  return values[target]
}
