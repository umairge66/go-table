import clsx from 'clsx'
import { Check, Search } from 'lucide-react'
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Kbd, Spinner } from './ui'

export interface Command {
  id: string
  label: string
  hint?: string
  icon?: ReactNode
  /** group puts the command under a heading (shown when not searching). */
  group?: string
  /** active marks the current item (e.g. the open database). */
  active?: boolean
  run: () => void
}

/** score ranks a fuzzy match: consecutive and word-start matches score higher. */
export function score(label: string, q: string): number {
  if (!q) return 1
  const l = label.toLowerCase()
  const s = q.toLowerCase()
  if (l === s) return 1000
  if (l.startsWith(s)) return 500 - l.length
  const i = l.indexOf(s)
  if (i >= 0) return 300 - i
  let pos = 0
  let pts = 0
  for (const ch of s) {
    const j = l.indexOf(ch, pos)
    if (j < 0) return 0
    pts += j === pos ? 3 : 1
    pos = j + 1
  }
  return pts
}

/**
 * CommandPalette is the searchable popup behind Ctrl+K (everything),
 * Ctrl+P (tables) and Ctrl+D (databases).
 */
export function CommandPalette({ commands, onClose, title, placeholder = 'Type a table name or a command…', emptyText = 'No matches', loading }: {
  commands: Command[]
  onClose: () => void
  title?: string
  placeholder?: string
  emptyText?: string
  loading?: boolean
}) {
  const [q, setQ] = useState('')
  const list = useRef<HTMLDivElement>(null)
  // Stable sort keeps the given order when nothing is typed.
  const items = useMemo(() => commands.map((c) => ({ c, s: score(c.label, q) })).filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s).slice(0, 200).map((x) => x.c), [commands, q])
  // Start on the current item, so Enter keeps it and arrows move from it.
  // Worked out while rendering (not in an effect), so the first frame
  // already shows it rather than flashing the first row.
  const startAt = () => { const a = q ? -1 : items.findIndex((c) => c.active); return a >= 0 ? a : 0 }
  const [i, setI] = useState(startAt)
  const listKey = `${q}\u0000${items.length}`
  const [seenKey, setSeenKey] = useState(listKey)
  if (seenKey !== listKey) { setSeenKey(listKey); setI(startAt()) }
  // Scroll before the browser paints, so the list opens already in place.
  useLayoutEffect(() => { list.current?.querySelector(`[data-i="${i}"]`)?.scrollIntoView({ block: 'nearest' }) }, [i])

  const run = (c?: Command) => { if (c) { onClose(); c.run() } }
  const showGroups = !q
  return createPortal(
    <div className="fixed inset-0 z-50 flex justify-center bg-black/25 pt-[14vh]" onMouseDown={onClose}>
      <div className="flex h-fit max-h-[60vh] w-[560px] flex-col overflow-hidden rounded-xl border border-line bg-panel shadow-pop animate-pop" onMouseDown={(e) => e.stopPropagation()}
        role="dialog" aria-label={title ?? 'Command palette'}>
        <div className="flex items-center gap-2 border-b border-line-2 px-3.5">
          <Search className="size-4 text-muted" />
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={placeholder} aria-label="Search commands"
            className="h-12 flex-1 bg-transparent text-[14px] outline-none placeholder:text-faint focus-visible:outline-none"
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setI((x) => Math.min(items.length - 1, x + 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setI((x) => Math.max(0, x - 1)) }
              else if (e.key === 'Enter') run(items[i])
              else if (e.key === 'Escape') onClose()
            }} />
          {loading ? <Spinner /> : <Kbd>Esc</Kbd>}
        </div>
        <div ref={list} className="overflow-y-auto p-1.5" role="listbox">
          {items.length === 0 && !loading && <div className="py-8 text-center text-[12.5px] text-muted">{emptyText}</div>}
          {items.map((c, idx) => (
            <div key={c.id}>
              {showGroups && c.group && c.group !== items[idx - 1]?.group && (
                <div className="px-2.5 pb-1 pt-2.5 text-[10.5px] font-semibold uppercase tracking-wider text-faint first:pt-1">{c.group}</div>
              )}
              <button data-i={idx} role="option" aria-selected={idx === i} onMouseMove={() => setI(idx)} onClick={() => run(c)}
                className={clsx('flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-[13px]', idx === i ? 'bg-accent text-accent-fg' : 'text-fg-2')}>
                <span className="flex w-4 justify-center [&>svg]:size-4">{c.icon}</span>
                <span className={clsx('flex-1 truncate', c.active && 'font-semibold')}>{c.label}</span>
                {c.active && <Check className={clsx('size-4', idx === i ? '' : 'text-accent')} aria-label="current" />}
                {c.hint && <span className={clsx('text-[11.5px]', idx === i ? 'opacity-80' : 'text-faint')}>{c.hint}</span>}
              </button>
            </div>
          ))}
        </div>
      </div>
    </div>,
    document.body,
  )
}
