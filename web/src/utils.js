export function formatBytes(bytes) {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

export function formatDuration(ms) {
  if (!ms) return '-'
  if (ms < 1000) return ms + 'ms'
  const seconds = ms / 1000
  if (seconds < 60) return seconds.toFixed(1) + 's'
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = Math.floor(seconds % 60)
  return `${minutes}m ${remainingSeconds}s`
}

export function formatDurationStr(str) {
  if (!str) return '-'
  // Handle Go duration strings like "1m5.912904792s", "45.123s", "500ms"
  // Check for standalone ms first (before the general regex eats "500m" from "500ms")
  const msOnly = str.match(/^(\d+)ms$/)
  if (msOnly) return str
  const match = str.match(/(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?/)
  if (!match) return str
  const h = parseInt(match[1]) || 0
  const m = parseInt(match[2]) || 0
  const s = Math.floor(parseFloat(match[3]) || 0)
  const ms = 0
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  if (s > 0) return `${s}s`
  if (ms > 0) return `${ms}ms`
  return str
}

export function truncateId(id, length = 8) {
  return id ? id.substring(0, length) + '...' : '-'
}
