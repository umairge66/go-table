import clsx from 'clsx'
import { CheckCircle2, Copy, FileCode2, GitCommitHorizontal, Pencil, Play, Search, Trash2, XCircle } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { useConfirm } from '../components/Confirm'
import { Button, EmptyState, Input, Modal, errorParts, useMenu, useToast } from '../components/ui'
import type { HistoryEntry, SavedQuery } from '../types'

/** Fired after something that changes the history or saved queries. */
export const HISTORY_EVENT = 'gotable:history-changed'
export const QUERIES_EVENT = 'gotable:queries-changed'
export const notify = (event: string) => window.dispatchEvent(new CustomEvent(event))

const firstLine = (sql: string) => sql.split('\n').map((l) => l.trim()).find((l) => l && !l.startsWith('--')) ?? sql.trim()

function ago(iso: string) {
  const d = new Date(iso)
  const s = (Date.now() - d.getTime()) / 1000
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function dayLabel(iso: string) {
  const d = new Date(iso)
  const today = new Date()
  const yesterday = new Date(Date.now() - 86400000)
  if (d.toDateString() === today.toDateString()) return 'Today'
  if (d.toDateString() === yesterday.toDateString()) return 'Yesterday'
  return d.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short', year: d.getFullYear() === today.getFullYear() ? undefined : 'numeric' })
}

/** QueriesPanel lists the connection's saved queries. */
export function QueriesPanel({ connectionId, dbLabel, onOpen }: {
  connectionId: string
  dbLabel: (q: { database: string; schema: string }) => string
  onOpen: (q: SavedQuery) => void
}) {
  const toast = useToast()
  const confirm = useConfirm()
  const menu = useMenu()
  const [items, setItems] = useState<SavedQuery[] | null>(null)
  const [filter, setFilter] = useState('')
  const [renaming, setRenaming] = useState<SavedQuery | null>(null)

  const load = useCallback(() => api.queries(connectionId).then(setItems).catch(() => setItems([])), [connectionId])
  useEffect(() => {
    load()
    window.addEventListener(QUERIES_EVENT, load)
    return () => window.removeEventListener(QUERIES_EVENT, load)
  }, [load])

  const shown = useMemo(() => {
    const f = filter.trim().toLowerCase()
    return (items ?? []).filter((q) => !f || q.name.toLowerCase().includes(f) || q.sql.toLowerCase().includes(f))
  }, [items, filter])

  const remove = async (q: SavedQuery) => {
    if (await confirm({ title: `Delete “${q.name}”?`, message: 'The saved query is removed. Open tabs keep their text.', confirmLabel: 'Delete', danger: true }) !== 'confirm') return
    await api.deleteQuery(q.id)
    notify(QUERIES_EVENT)
  }
  const duplicate = async (q: SavedQuery) => {
    try {
      await api.saveQuery({ ...q, id: crypto.randomUUID(), name: `${q.name} copy` })
      notify(QUERIES_EVENT)
    } catch (e) { toast('error', ...errorParts(e)) }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-2.5 pb-2"><Input icon={<Search />} value={filter} placeholder="Search saved queries" aria-label="Search saved queries" onChange={(e) => setFilter(e.target.value)} /></div>
      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-3" aria-label="Saved queries" role="list">
        {items && items.length === 0 && (
          <EmptyState icon={<FileCode2 />} title="No saved queries">Write a query and press <b>Ctrl+S</b> to keep it here.</EmptyState>
        )}
        {items && items.length > 0 && shown.length === 0 && <div className="p-4 text-center text-[12px] text-muted">Nothing matches “{filter}”.</div>}
        {shown.map((q) => (
          <button key={q.id} role="listitem" onClick={() => onOpen(q)} title={q.sql}
            onContextMenu={(e) => menu.open(e, [
              { label: 'Open', icon: <FileCode2 />, onSelect: () => onOpen(q) },
              { label: 'Rename…', icon: <Pencil />, onSelect: () => setRenaming(q) },
              { label: 'Duplicate', icon: <Copy />, onSelect: () => duplicate(q) },
              { label: 'Copy SQL', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(q.sql) },
              'sep',
              { label: 'Delete…', icon: <Trash2 />, danger: true, onSelect: () => remove(q) },
            ])}
            className="flex w-full flex-col gap-0.5 rounded-md px-2 py-1.5 text-left hover:bg-hover">
            <span className="flex items-center gap-1.5 text-[13px] font-medium text-fg-2"><FileCode2 className="size-3.5 shrink-0 text-accent" /><span className="truncate">{q.name}</span></span>
            <span className="truncate pl-5 font-mono text-[11px] text-muted">{firstLine(q.sql)}</span>
            <span className="truncate pl-5 text-[10.5px] text-faint">{dbLabel(q)} · {ago(q.updated)}</span>
          </button>
        ))}
      </div>
      {renaming && <NameDialog title="Rename query" initial={renaming.name} confirmLabel="Rename" onClose={() => setRenaming(null)}
        onSubmit={async (name) => {
          await api.saveQuery({ ...renaming, name })
          setRenaming(null)
          notify(QUERIES_EVENT)
        }} />}
      {menu.node}
    </div>
  )
}

/** HistoryPanel lists executed statements, newest first, grouped by day. */
export function HistoryPanel({ connectionId, dbLabel, onOpen }: {
  connectionId: string
  dbLabel: (h: { database: string; schema: string }) => string
  onOpen: (h: HistoryEntry, run: boolean) => void
}) {
  const confirm = useConfirm()
  const menu = useMenu()
  const [items, setItems] = useState<HistoryEntry[] | null>(null)
  const [filter, setFilter] = useState('')

  const load = useCallback(() => api.history(connectionId, filter).then(setItems).catch(() => setItems([])), [connectionId, filter])
  useEffect(() => {
    const t = setTimeout(load, 150)
    window.addEventListener(HISTORY_EVENT, load)
    return () => { clearTimeout(t); window.removeEventListener(HISTORY_EVENT, load) }
  }, [load])

  const clear = async () => {
    if (await confirm({ title: 'Clear history?', message: 'All recorded queries of this connection are removed. Saved queries are kept.', confirmLabel: 'Clear history', danger: true }) !== 'confirm') return
    await api.clearHistory(connectionId)
    load()
  }

  let lastDay = ''
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex gap-1.5 px-2.5 pb-2">
        <Input className="flex-1" icon={<Search />} value={filter} placeholder="Search history" aria-label="Search history" onChange={(e) => setFilter(e.target.value)} />
        {!!items?.length && <Button size="sm" variant="ghost" onClick={clear} title="Clear history">Clear</Button>}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-3" aria-label="Query history" role="list">
        {items && items.length === 0 && (
          <EmptyState icon={<FileCode2 />} title={filter ? 'Nothing found' : 'No history yet'}>
            {filter ? `No query contains “${filter}”.` : 'Queries you run and changes you commit appear here.'}
          </EmptyState>
        )}
        {(items ?? []).map((h) => {
          const day = dayLabel(h.at)
          const header = day !== lastDay ? day : null
          lastDay = day
          return (
            <div key={h.id}>
              {header && <div className="px-2 pb-1 pt-2.5 text-[10.5px] font-semibold uppercase tracking-wider text-faint">{header}</div>}
              <button role="listitem" onClick={() => onOpen(h, false)} title={h.error ? `${h.sql}\n\n${h.error}` : h.sql}
                onContextMenu={(e) => menu.open(e, [
                  { label: 'Open in new query', icon: <FileCode2 />, onSelect: () => onOpen(h, false) },
                  { label: 'Run again', icon: <Play />, onSelect: () => onOpen(h, true) },
                  { label: 'Copy SQL', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(h.sql) },
                  'sep',
                  { label: 'Clear history…', icon: <Trash2 />, danger: true, onSelect: clear },
                ])}
                className="flex w-full gap-2 rounded-md px-2 py-1.5 text-left hover:bg-hover">
                {h.kind === 'commit'
                  ? <GitCommitHorizontal className={clsx('mt-0.5 size-3.5 shrink-0', h.error ? 'text-err' : 'text-warn')} aria-label="commit" />
                  : h.error ? <XCircle className="mt-0.5 size-3.5 shrink-0 text-err" aria-label="failed" /> : <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-ok" aria-label="succeeded" />}
                <span className="min-w-0 flex-1">
                  <span className="line-clamp-2 [overflow-wrap:anywhere] font-mono text-[11.5px] leading-snug text-fg-2">{firstLine(h.sql)}</span>
                  <span className="mt-0.5 block truncate text-[10.5px] text-faint">
                    {new Date(h.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })} · {h.ms < 1 ? '<1' : Math.round(h.ms)} ms
                    {h.error ? ' · failed' : h.kind === 'commit' ? ` · ${h.rows} change${h.rows === 1 ? '' : 's'}` : ` · ${h.rows.toLocaleString()} row${h.rows === 1 ? '' : 's'}`} · {dbLabel(h)}
                  </span>
                </span>
              </button>
            </div>
          )
        })}
      </div>
      {menu.node}
    </div>
  )
}

/** NameDialog asks for a name (saving or renaming a query). */
export function NameDialog({ title, initial, confirmLabel, onClose, onSubmit }: {
  title: string; initial: string; confirmLabel: string; onClose: () => void; onSubmit: (name: string) => Promise<void> | void
}) {
  const toast = useToast()
  const [name, setName] = useState(initial)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    if (!name.trim()) return
    setBusy(true)
    try { await onSubmit(name.trim()) } catch (e) { toast('error', ...errorParts(e)) } finally { setBusy(false) }
  }
  return (
    <Modal width={440} title={title} onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} disabled={!name.trim()} onClick={go}>{confirmLabel}</Button></>}>
      <Input autoFocus value={name} aria-label="Query name" onChange={(e) => setName(e.target.value)} onFocus={(e) => e.target.select()}
        onKeyDown={(e) => e.key === 'Enter' && go()} />
    </Modal>
  )
}
