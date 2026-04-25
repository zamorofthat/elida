import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { SearchInput } from './shared/SearchInput'
import { IconEmpty } from './shared/Icons'
import { SessionRow } from './SessionRow'
import { SessionDetailModal } from './SessionDetailModal'

export function SessionsPage() {
  const [sessions, setSessions] = useState([])
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState('all') // all | live | flagged
  const [detailSession, setDetailSession] = useState(null)
  const timerRef = useRef(null)

  const fetchSessions = async () => {
    try {
      const res = await apiFetch('/control/sessions')
      const data = await res.json()
      setSessions(data.sessions || [])
    } catch (err) {
      console.error('Failed to fetch sessions:', err)
    }
  }

  useEffect(() => {
    fetchSessions()
    timerRef.current = setInterval(() => {
      if (!document.hidden) fetchSessions()
    }, 5000)
    return () => clearInterval(timerRef.current)
  }, [])

  const killSession = async (id) => {
    if (!confirm('Kill this session?')) return
    try {
      await apiFetch('/control/sessions/' + id + '/kill', { method: 'POST' })
      fetchSessions()
    } catch {
      alert('Failed to kill session')
    }
  }

  // Filter and search
  let filtered = sessions
  if (filter === 'live') {
    filtered = filtered.filter(s => s.state === 'active')
  } else if (filter === 'flagged') {
    filtered = filtered.filter(s => (s.risk_score || 0) > 0)
  }

  if (search) {
    const term = search.toLowerCase()
    filtered = filtered.filter(s =>
      s.id?.toLowerCase().includes(term) ||
      s.client_addr?.toLowerCase().includes(term) ||
      s.state?.toLowerCase().includes(term) ||
      s.backend?.toLowerCase().includes(term) ||
      (s.backends_used && Object.keys(s.backends_used).some(b => b.toLowerCase().includes(term)))
    )
  }

  // Sort: active sessions pinned to top, then by start_time desc
  const sorted = [...filtered].sort((a, b) => {
    if (a.state === 'active' && b.state !== 'active') return -1
    if (a.state !== 'active' && b.state === 'active') return 1
    const ta = a.start_time ? new Date(a.start_time).getTime() : 0
    const tb = b.start_time ? new Date(b.start_time).getTime() : 0
    return tb - ta
  })

  const filters = [
    { id: 'all', label: 'All' },
    { id: 'live', label: 'Live' },
    { id: 'flagged', label: 'Flagged' },
  ]

  return (
    <div>
      <div class="sessions-toolbar">
        <div class="sessions-filters">
          {filters.map(f => (
            <button
              key={f.id}
              class={'filter-chip' + (filter === f.id ? ' active' : '')}
              onClick={() => setFilter(f.id)}
            >
              {f.label}
            </button>
          ))}
        </div>
        <SearchInput value={search} onChange={setSearch} placeholder="Search sessions..." />
      </div>

      <div class="sessions-list">
        {sorted.length === 0 ? (
          <div class="empty-state">
            <IconEmpty />
            <p>{search ? 'No matching sessions' : 'No sessions'}</p>
          </div>
        ) : (
          sorted.map(s => (
            <SessionRow key={s.id} session={s} onKill={killSession} onViewDetail={setDetailSession} />
          ))
        )}
      </div>

      {detailSession && (
        <SessionDetailModal
          session={detailSession}
          onClose={() => setDetailSession(null)}
          onKill={(id) => { killSession(id); setDetailSession(null); }}
        />
      )}
    </div>
  )
}
