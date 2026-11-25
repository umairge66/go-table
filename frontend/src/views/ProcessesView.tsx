import clsx from 'clsx'
import { Activity, Copy, OctagonX, Plug, Search, Square } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { useConfirm } from '../components/Confirm'
import { Button, IconButton, Input, RefreshButton, Select, Spinner, Switch, errorParts, useMenu, useToast } from '../components/ui'
import { formatSQL } from '../lib/format'
import { family, usesCommands, type Process, type SessionInfo } from '../types'

const EVERY = [1, 2, 5, 10, 30]
const SLOW = 5 // seconds from which a query is marked

export function duration(s: number) {
  if (s < 10) return `${s.toFixed(1)} s`
  if (s < 120) return `${Math.round(s)} s`
  if (s < 7200) return `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`
  if (s < 172800) return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`
  return `${Math.floor(s / 86400)} days`
}

type SortBy = 'time' | 'user' | 'database'

/** ProcessesView shows what the server runs, and stops it. */
export function ProcessesView({ session, active, openQuery }: { session: SessionInfo; active: boolean; openQuery: (sql: string) => void }) {
  const toast = useToast()
  const confirm = useConfirm()
  const menu = useMenu()
  const [list, setList] = useState<{ processes: Process[]; canStop: boolean; canKill: boolean } | null>(null)
  const [error, setError] = useState<[string, string] | null>(null)
  const [busy, setBusy] = useState(false)
  const [auto, setAuto] = useState(true)
  const [every, setEvery] = useState(() => Number(localStorage.getItem('gotable:procEvery')) || 2)
  const [idle, setIdle] = useState(false)
  const [search, setSearch] = useState('')
  const [by, setBy] = useState<SortBy>('time')
  const [sel, setSel] = useState<string | null>(null)
  const redis = family(session.connection.driver) === 'redis'
  const loading = useRef(false)
  const load = useCallback(async () => {
    if (loading.current) return
    loading.current = true
    setBusy(true)
    try {
      setList(await api.processes(session.id))
      setError(null)
    } catch (e) { setError(errorParts(e)) } finally { loading.current = false; setBusy(false) }
  }, [session.id])
  useEffect(() => { if (active) load() }, [active, load])
  // Only while it is looked at: each reading is a query.
  useEffect(() => {
    if (!active || !auto || error) return
    const t = setInterval(() => { if (!document.hidden) load() }, every * 1000)
    return () => clearInterval(t)
  }, [active, auto, every, load, error])

  const shown = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (list?.processes ?? [])
      .filter((p) => idle || redis || p.active)
      .filter((p) => !q || [p.id, p.user, p.client, p.app, p.database, p.state, p.query].some((x) => x.toLowerCase().includes(q)))
      .sort((a, b) => Number(b.active) - Number(a.active) || (by === 'time' ? b.seconds - a.seconds : by === 'user' ? a.user.localeCompare(b.user) : a.database.localeCompare(b.database)) || a.id.localeCompare(b.id))
  }, [list, idle, redis, search, by])
  const current = list?.processes.find((p) => p.id === sel)
  const running = list?.processes.filter((p) => p.active).length ?? 0
  const blocking = useMemo(() => new Set(list?.processes.flatMap((p) => p.blockedBy) ?? []), [list])

  const end = async (p: Process, kill: boolean) => {
    const r = await confirm(kill
      ? { title: `Close connection ${p.id}?`, message: <>What it runs is stopped, and its open transaction is rolled back. {p.app === 'GoTable' && 'It may be one of GoTable’s own.'}{p.query && <code className="mt-2 block max-h-32 overflow-auto break-words rounded-lg bg-hover p-2 font-mono text-[12px]">{p.query.slice(0, 600)}</code>}</>, confirmLabel: 'Close connection', danger: true }
      : { title: 'Stop this query?', message: <>The connection stays open.<code className="mt-2 block max-h-32 overflow-auto break-words rounded-lg bg-hover p-2 font-mono text-[12px]">{p.query.slice(0, 600)}</code></>, confirmLabel: 'Stop query', danger: true })
    if (r !== 'confirm') return
    try {
      await api.stopProcess(session.id, p.id, kill)
      toast('ok', kill ? 'Connection closed' : 'Query stopped', `${p.user} · ${p.id}`)
    } catch (e) { toast('error', ...errorParts(e)) }
    load()
  }
  const text = (p: Process) => { try { return usesCommands(session.connection.driver) ? p.query : formatSQL(p.query, session.connection.driver) } catch { return p.query } }
  const rowMenu = (e: React.MouseEvent, p: Process) => {
    setSel(p.id)
    menu.open(e, [
      ...(p.query ? [{ label: 'Copy query', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(p.query) }] : []),
      ...(p.query && !redis ? [{ label: 'Open in a query tab', onSelect: () => openQuery(text(p)) }] : []),
      ...(list?.canStop && p.active ? ['sep' as const, { label: 'Stop query…', icon: <Square />, danger: true, onSelect: () => end(p, false) }] : []),
      ...(list?.canKill ? [{ label: 'Close connection…', icon: <OctagonX />, danger: true, onSelect: () => end(p, true) }] : []),
    ])
  }
  const heads: [string, string, SortBy?][] = [['Time', 'w-28', 'time'], ['Id', 'w-24'], ['User', 'w-32', 'user'], ['Database', 'w-36', 'database'], ['State', 'w-44'], ['Query', '']]

  return (
    <div className="flex h-full flex-col" data-testid="processes">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3">
        <Input className="w-64 min-w-20" icon={<Search />} value={search} placeholder="Filter by user, query…" aria-label="Filter processes" onChange={(e) => setSearch(e.target.value)} />
        {!redis && <Switch checked={idle} onChange={setIdle} label={<span className="whitespace-nowrap text-[12.5px]">Idle connections</span>} />}
        <div className="flex-1" />
        {list && <span className="whitespace-nowrap text-[12px] text-muted"><b className="font-semibold text-fg-2">{redis ? list.processes.length : running}</b> {redis ? 'connected' : `running · ${list.processes.length} connected`}</span>}
        <Switch checked={auto} onChange={setAuto} label={<span className="text-[12.5px]">Every</span>} />
        <Select aria-label="Refresh interval" className="w-20 shrink-0" value={every} disabled={!auto} onChange={(e) => { setEvery(Number(e.target.value)); localStorage.setItem('gotable:procEvery', e.target.value) }}>
          {EVERY.map((n) => <option key={n} value={n}>{n} s</option>)}
        </Select>
        <RefreshButton label="Refresh" onRefresh={load} busy={busy && !auto} />
      </div>
      {error ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
          <div className="text-[14px] font-semibold text-err">{error[0]}</div>
          <div className="max-w-xl break-words text-[12.5px] text-muted">{error[1]}</div>
          <Button size="sm" className="mt-2" onClick={load}>Try again</Button>
        </div>
      ) : !list ? <div className="flex flex-1 items-center justify-center"><Spinner className="size-5" /></div> : (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="min-h-0 flex-1 overflow-auto">
            <table className="w-full table-fixed text-[12.5px]">
              <thead className="sticky top-0 z-10 bg-panel-2 text-left text-[11.5px] text-muted">
                <tr>
                  {heads.map(([h, w, s]) => (
                    <th key={h} className={clsx('border-b border-line px-3 py-2 font-semibold', w)} aria-sort={s === by ? 'descending' : undefined}>
                      {s ? <button className={clsx('hover:text-fg', s === by && 'text-accent')} onClick={() => setBy(s)}>{h === 'Time' && redis ? 'Idle for' : h}</button> : h === 'Query' && redis ? 'Last command' : h}
                    </th>
                  ))}
                  <th className="w-24 border-b border-line" />
                </tr>
              </thead>
              <tbody>
                {shown.map((p) => {
                  const slow = p.active && p.seconds >= SLOW
                  return (
                    <tr key={p.id} data-testid="process" data-id={p.id} aria-selected={sel === p.id} onClick={() => setSel(p.id === sel ? null : p.id)} onContextMenu={(e) => rowMenu(e, p)}
                      className={clsx('group cursor-pointer border-b border-line-2', sel === p.id ? 'bg-selected' : 'hover:bg-hover', !p.active && !redis && 'text-muted')}>
                      <td className="px-3 py-1.5">
                        <span className={clsx('inline-flex items-center gap-1.5 tabular-nums', slow && 'font-semibold', slow && (p.seconds >= 60 ? 'text-err' : 'text-warn'))}>
                          <span className={clsx('size-1.5 shrink-0 rounded-full', !p.active ? 'bg-line' : slow ? (p.seconds >= 60 ? 'bg-err' : 'bg-warn') : 'bg-ok')} aria-hidden="true" />
                          {duration(p.seconds)}
                        </span>
                      </td>
                      <td className="truncate px-3 py-1.5 font-mono text-[12px] text-muted" title={p.id}>{p.id}</td>
                      <td className="truncate px-3 py-1.5" title={[p.user, p.client, p.app].filter(Boolean).join('\n')}>{p.user || '—'}</td>
                      <td className="truncate px-3 py-1.5" title={p.database}>{p.database || '—'}</td>
                      <td className="px-3 py-1.5">
                        <div className="truncate" title={[p.state, p.wait].filter(Boolean).join('\n')}>{p.state}{p.wait && p.active && <span className="text-muted"> · {p.wait}</span>}</div>
                        {p.blockedBy.length > 0 && <div className="truncate text-[11.5px] font-medium text-err">blocked by {p.blockedBy.join(', ')}</div>}
                        {blocking.has(p.id) && <div className="text-[11.5px] font-medium text-warn">blocks others</div>}
                      </td>
                      <td className="truncate px-3 py-1.5 font-mono text-[12px]" title={p.query.slice(0, 1500)}>{p.query.replace(/\s+/g, ' ').slice(0, 400)}</td>
                      <td className="px-2 py-1 text-right" onClick={(e) => e.stopPropagation()}>
                        <span className="inline-flex gap-0.5 opacity-60 group-hover:opacity-100">
                          {list.canStop && p.active && <IconButton label="Stop query" className="enabled:hover:!text-err" onClick={() => end(p, false)}><Square className="size-3.5" /></IconButton>}
                          {list.canKill && <IconButton label="Close connection" className="enabled:hover:!text-err" onClick={() => end(p, true)}><OctagonX className="size-3.5" /></IconButton>}
                        </span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
            {shown.length === 0 && (
              <div className="flex flex-col items-center gap-2 py-14 text-center text-[13px] text-muted"><Activity className="size-8 text-faint" />{search ? `Nothing matches “${search}”.` : 'Nothing is running right now.'}</div>
            )}
          </div>
          {current && (
            <div data-bottom-bar className="max-h-[45%] shrink-0 overflow-auto border-t border-line bg-panel-2 p-3 animate-fade" data-testid="process-detail">
              <div className="mb-2 flex items-center gap-2 text-[12px]">
                <Plug className="size-3.5 text-muted" />
                <span className="select-text font-medium">{[current.user, current.client, current.app].filter(Boolean).join(' · ') || current.id}</span>
                <span className="text-muted">{current.active ? `running for ${duration(current.seconds)}` : `idle for ${duration(current.seconds)}`}{current.wait && ` · ${current.wait}`}</span>
                <div className="flex-1" />
                {current.query && !redis && <Button size="sm" variant="ghost" onClick={() => openQuery(text(current))}>Open in a query tab</Button>}
                {list.canStop && current.active && <Button size="sm" icon={<Square className="size-3 fill-current" />} onClick={() => end(current, false)}>Stop query</Button>}
                {list.canKill && <Button size="sm" variant="danger" onClick={() => end(current, true)}>Close connection</Button>}
              </div>
              <pre className="select-text whitespace-pre-wrap break-words rounded-lg border border-line bg-panel p-2.5 font-mono text-[12px]">{current.query ? text(current) : 'It runs nothing.'}</pre>
            </div>
          )}
        </div>
      )}
      {menu.node}
    </div>
  )
}
