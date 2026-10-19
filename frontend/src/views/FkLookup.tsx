import clsx from 'clsx'
import { Check, Search } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { api } from '../api'
import { Input, Spinner } from '../components/ui'
import type { Cell, Column, ForeignRef } from '../types'

const LIMIT = 50
const TEXTY = /char|text|name|string|enum|citext/i
const NICE = /^(name|title|label|display_?name|full_?name|username|email|code|slug)$/i

/** labelColumn picks a human-readable column of the referenced table. */
export function labelColumn(cols: Column[], keys: string[]): string | undefined {
  const rest = cols.filter((c) => !keys.includes(c.name))
  return (rest.find((c) => NICE.test(c.name)) ?? rest.find((c) => TEXTY.test(c.type)))?.name
}

interface Option { values: Cell[]; label: Cell }

/**
 * FkLookup is the dropdown shown when editing a foreign key cell: it searches
 * the referenced table and sets every column of the key at once.
 */
export function FkLookup({ sid, fk, current, anchor, nullable, onPick, onText, onClose }: {
  sid: string
  fk: ForeignRef
  /** current holds this row's values of fk.from. */
  current: Cell[]
  anchor: DOMRect
  nullable: boolean
  onPick: (values: Cell[]) => void
  onText: () => void
  onClose: () => void
}) {
  const box = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const [q, setQ] = useState('')
  const [label, setLabel] = useState<string | null | undefined>(undefined) // undefined: loading
  const [options, setOptions] = useState<Option[] | null>(null)
  const [total, setTotal] = useState(0)
  const [error, setError] = useState('')
  const [hi, setHi] = useState(0)
  const table = { schema: fk.schema, name: fk.table }

  useEffect(() => {
    api.structure(sid, table).then((st) => setLabel(labelColumn(st.columns, fk.columns) ?? null)).catch(() => setLabel(null))
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (label === undefined) return
    const ctl = new AbortController()
    const cols = label ? [...fk.columns, label] : fk.columns
    const t = setTimeout(() => {
      api.rows(sid, table, {
        filters: q.trim() ? [{ column: '*', op: 'Contains', value: q.trim(), value2: '' }] : [],
        rawWhere: '', sort: fk.columns.map((column) => ({ column, dir: 'asc' as const })), limit: LIMIT, offset: 0,
      }, cols, ctl.signal).then((r) => {
        const idx = fk.columns.map((c) => r.columns.indexOf(c))
        const li = label ? r.columns.indexOf(label) : -1
        setOptions(r.rows.map((row) => ({ values: idx.map((i) => row[i]), label: li >= 0 ? row[li] : null })))
        setTotal(r.total)
        setHi(0)
        setError('')
      }).catch((e) => { if (!ctl.signal.aborted) setError(e instanceof Error ? e.message : String(e)) })
    }, q ? 180 : 0)
    return () => { clearTimeout(t); ctl.abort() }
  }, [q, label]) // eslint-disable-line react-hooks/exhaustive-deps

  // Close on a click elsewhere.
  useEffect(() => {
    const down = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) onClose() }
    window.addEventListener('mousedown', down)
    return () => window.removeEventListener('mousedown', down)
  }, [onClose])

  // Keep the dropdown on screen: below the cell, or above when there's no room.
  const [pos, setPos] = useState({ top: anchor.bottom + 2, left: anchor.left })
  useLayoutEffect(() => {
    const h = box.current?.offsetHeight ?? 360
    const w = box.current?.offsetWidth ?? 360
    const top = anchor.bottom + 2 + h > window.innerHeight - 8 ? Math.max(8, anchor.top - h - 2) : anchor.bottom + 2
    setPos({ top, left: Math.max(8, Math.min(anchor.left, window.innerWidth - w - 8)) })
  }, [anchor, options])

  useEffect(() => {
    list.current?.querySelector(`[data-i="${hi}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [hi])

  const same = (o: Option) => o.values.every((v, i) => v === current[i])
  const keyText = (o: Option) => o.values.map((v) => v ?? 'NULL').join(', ')

  const onKey = (e: React.KeyboardEvent) => {
    e.stopPropagation()
    const n = options?.length ?? 0
    if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => Math.min(n - 1, h + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(0, h - 1)) }
    else if (e.key === 'Enter') { e.preventDefault(); const o = options?.[hi]; if (o) onPick(o.values) }
    else if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }

  const target = fk.columns.length > 1 ? `${fk.table}(${fk.columns.join(', ')})` : `${fk.table}.${fk.columns[0]}`
  return (
    <div ref={box} role="dialog" aria-label={`Choose ${target}`} onKeyDown={onKey}
      className="fixed z-[70] flex max-h-[360px] w-[360px] flex-col rounded-lg border border-line bg-panel font-sans shadow-pop animate-pop"
      style={{ top: pos.top, left: pos.left, minWidth: Math.min(anchor.width, 480) }}>
      <div className="border-b border-line p-1.5">
        <Input autoFocus icon={<Search />} value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search referenced rows"
          placeholder={`Search ${fk.table}${label ? ` by ${fk.columns.join(', ')} or ${label}` : ''}…`} />
      </div>
      <div ref={list} role="listbox" aria-label={target} className="min-h-0 flex-1 overflow-y-auto p-1">
        {options === null && !error && <div className="flex justify-center p-4"><Spinner /></div>}
        {error && <div className="p-3 text-[12px] text-err">{error}</div>}
        {options?.length === 0 && <div className="p-3 text-center text-[12px] text-muted">No rows in {fk.table} match “{q}”.</div>}
        {options?.map((o, i) => (
          <div key={i} data-i={i} role="option" aria-selected={i === hi} title={keyText(o) + (o.label ? ` · ${o.label}` : '')}
            onMouseEnter={() => setHi(i)} onMouseDown={(e) => e.preventDefault()} onClick={() => onPick(o.values)}
            className={clsx('flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-[12.5px]', i === hi && 'bg-hover')}>
            <span className="shrink-0 font-mono tabular-nums text-fg">{keyText(o)}</span>
            {o.label !== null && <span className="min-w-0 flex-1 truncate text-muted">{o.label}</span>}
            {same(o) && <Check className="ml-auto size-3.5 shrink-0 text-accent" aria-label="current" />}
          </div>
        ))}
      </div>
      <div className="flex items-center gap-1 border-t border-line px-2 py-1.5 text-[11.5px] text-muted">
        <span className="mr-auto">{options ? (total > options.length ? `${options.length} of ${total.toLocaleString()} — type to narrow` : `${total.toLocaleString()} row${total === 1 ? '' : 's'}`) : ''}</span>
        {nullable && <button className="rounded px-1.5 py-0.5 hover:bg-hover hover:text-fg" onClick={() => onPick(current.map(() => null))}>Set NULL</button>}
        <button className="rounded px-1.5 py-0.5 hover:bg-hover hover:text-fg" onClick={onText}>Edit as text</button>
      </div>
    </div>
  )
}
