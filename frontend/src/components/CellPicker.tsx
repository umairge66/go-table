import clsx from 'clsx'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useEffect, useLayoutEffect, useState, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { fmtDate, fmtDateTime, fmtTime, nowParts, parseDate, parseTime, setMembers, type CellKind } from '../lib/cellKind'
import type { Cell } from '../types'

export type PickerKind = Exclude<CellKind, { kind: 'text' }>

/**
 * CellPicker opens under a cell's text editor: a calendar, time fields,
 * a list of choices or checkboxes. The text stays editable; the picker
 * writes into it (onChange) or finishes the edit (onCommit).
 */
export function CellPicker({ spec, anchor, pickerRef, value, nullable, choices, highlight, onChange, onCommit, onKeyDown, onFocusOut }: {
  spec: PickerKind
  anchor: RefObject<HTMLElement | null>
  pickerRef: RefObject<HTMLDivElement | null>
  value: string
  nullable: boolean
  /** choices: the options shown for a choice column (filtered by the typed text). */
  choices: { value: string; label: string }[]
  highlight: number
  onChange: (v: string) => void
  onCommit: (v: Cell) => void
  onKeyDown: (e: KeyboardEvent) => void
  onFocusOut: (to: EventTarget | null) => void
}) {
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const a = anchor.current?.getBoundingClientRect()
    const el = pickerRef.current
    if (!a || !el) return
    const r = el.getBoundingClientRect()
    const below = a.bottom + 4
    const top = below + r.height > window.innerHeight - 8 && a.top - 4 - r.height > 8 ? a.top - 4 - r.height : below
    setPos({ left: Math.max(8, Math.min(a.left, window.innerWidth - r.width - 8)), top })
  }, [anchor, pickerRef, spec])

  let body: ReactNode
  let footer: ReactNode = null
  const nullButton = nullable && <FooterButton onClick={() => onCommit(null)}>NULL</FooterButton>
  if (spec.kind === 'choice') {
    body = (
      <div role="listbox" aria-label="Choices" className="max-h-64 overflow-y-auto p-1">
        {choices.map((o, i) => (
          <button key={o.value} type="button" role="option" aria-selected={i === highlight} onClick={() => onCommit(o.value)}
            className={clsx('flex h-7 w-full items-center rounded-md px-2 text-left text-[12.5px]',
              i === highlight ? 'bg-accent text-accent-fg' : 'hover:bg-hover', o.value === value && i !== highlight && 'font-semibold text-accent')}>
            <span className="truncate">{o.label}</span>
          </button>
        ))}
        {!choices.length && <div className="px-2 py-1.5 text-[12px] text-muted">No match — Enter keeps the typed text.</div>}
        {nullable && (
          <button type="button" role="option" aria-selected={false} onClick={() => onCommit(null)}
            className="flex h-7 w-full items-center rounded-md px-2 text-left font-sans text-[11px] font-medium uppercase tracking-wide text-faint hover:bg-hover">null</button>
        )}
      </div>
    )
  } else if (spec.kind === 'set') {
    const members = setMembers(value)
    body = (
      <div className="max-h-64 overflow-y-auto p-1" role="group" aria-label="Members">
        {spec.options.map((o) => (
          <label key={o} className="flex h-7 cursor-pointer items-center gap-2 rounded-md px-2 text-[12.5px] hover:bg-hover">
            <input type="checkbox" className="accent-[var(--accent)]" checked={members.includes(o)}
              onChange={(e) => {
                const next = new Set(members)
                if (e.target.checked) next.add(o)
                else next.delete(o)
                onChange(spec.options.filter((x) => next.has(x)).join(','))
              }} />
            {o || '(empty)'}
          </label>
        ))}
      </div>
    )
    footer = <>{nullButton}<div className="flex-1" /><FooterButton primary onClick={() => onCommit(value)}>Done</FooterButton></>
  } else if (spec.kind === 'time') {
    const cur = parseTime(value)
    const base = cur ?? { h: 0, mi: 0, s: 0, frac: '', tz: '' }
    body = (
      <div className="flex items-center justify-center p-3">
        <TimeFields value={cur} onChange={(t) => onChange(fmtTime({ ...t, frac: '' }) + base.tz)} />
      </div>
    )
    footer = <>
      <FooterButton onClick={() => { const n = nowParts(); onChange(fmtTime(n) + base.tz) }}>Now</FooterButton>
      {nullButton}<div className="flex-1" />
      <FooterButton primary onClick={() => onCommit(value)}>Done</FooterButton>
    </>
  } else {
    const cur = parseDate(value)
    const base = cur ?? { ...nowParts(), h: 0, mi: 0, s: 0 }
    const withTime = spec.kind === 'datetime'
    body = (
      <div className="p-2">
        <Calendar value={cur} onPick={(d) => (withTime ? onChange(fmtDateTime({ ...base, ...d })) : onCommit(fmtDate(d)))} />
        {withTime && (
          <div className="mt-2 flex items-center justify-center gap-2 border-t border-line-2 pt-2">
            <span className="text-[11.5px] text-muted">Time</span>
            <TimeFields value={cur} onChange={(t) => onChange(fmtDateTime({ ...base, ...t, frac: '' }))} />
          </div>
        )}
      </div>
    )
    footer = withTime ? <>
      <FooterButton onClick={() => onChange(fmtDateTime({ ...nowParts(), tz: cur?.tz ?? '' }))}>Now</FooterButton>
      {nullButton}<div className="flex-1" />
      <FooterButton primary onClick={() => onCommit(value)}>Done</FooterButton>
    </> : <>
      <FooterButton onClick={() => onCommit(fmtDate(nowParts()))}>Today</FooterButton>
      {nullButton}
    </>
  }

  return createPortal(
    <div
      ref={pickerRef}
      data-testid="cell-picker"
      className="fixed z-[70] min-w-[200px] rounded-lg border border-line bg-panel font-sans shadow-pop"
      style={pos ? { left: pos.left, top: pos.top } : { left: -9999, top: 0 }}
      // Clicks keep the focus in the text field (except in the time fields);
      // events stay out of the grid, which is this component's React parent.
      onMouseDown={(e) => { e.stopPropagation(); if (!(e.target instanceof HTMLInputElement)) e.preventDefault() }}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.stopPropagation()}
      onKeyDown={(e) => { e.stopPropagation(); onKeyDown(e) }}
      onBlur={(e) => onFocusOut(e.relatedTarget)}
    >
      {body}
      {footer && <div className="flex items-center gap-1 border-t border-line-2 p-1.5">{footer}</div>}
    </div>,
    document.body,
  )
}

function FooterButton({ primary, onClick, children }: { primary?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick}
      className={clsx('h-6 rounded-md px-2 text-[12px] font-medium', primary ? 'bg-accent text-accent-fg hover:brightness-110' : 'text-fg-2 hover:bg-hover')}>
      {children}
    </button>
  )
}

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
const WEEKDAYS = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su']

function Calendar({ value, onPick }: { value: { y: number; m: number; d: number } | null; onPick: (d: { y: number; m: number; d: number }) => void }) {
  const today = nowParts()
  const [view, setView] = useState(() => ({ y: value?.y ?? today.y, m: value?.m ?? today.m }))
  // Typing a date in the text field shows its month.
  useEffect(() => { if (value) setView({ y: value.y, m: value.m }) }, [value?.y, value?.m]) // eslint-disable-line react-hooks/exhaustive-deps
  const step = (n: number) => setView((v) => {
    const i = v.y * 12 + (v.m - 1) + n
    return { y: Math.floor(i / 12), m: (i % 12) + 1 }
  })
  const first = new Date(view.y, view.m - 1, 1)
  const lead = (first.getDay() + 6) % 7 // Monday first
  const days = Array.from({ length: 42 }, (_, i) => {
    const dt = new Date(view.y, view.m - 1, 1 - lead + i)
    return { y: dt.getFullYear(), m: dt.getMonth() + 1, d: dt.getDate() }
  })
  const same = (a: { y: number; m: number; d: number } | null, b: { y: number; m: number; d: number }) => !!a && a.y === b.y && a.m === b.m && a.d === b.d
  return (
    <div className="w-[230px]">
      <div className="mb-1 flex items-center">
        <button type="button" aria-label="Previous month" onClick={() => step(-1)} className="flex size-7 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg"><ChevronLeft className="size-4" /></button>
        <div className="flex-1 text-center text-[12.5px] font-semibold" aria-live="polite">{MONTHS[view.m - 1]} {view.y}</div>
        <button type="button" aria-label="Next month" onClick={() => step(1)} className="flex size-7 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg"><ChevronRight className="size-4" /></button>
      </div>
      <div className="grid grid-cols-7 text-center text-[11px] text-faint">
        {WEEKDAYS.map((w) => <div key={w} className="py-1">{w}</div>)}
      </div>
      <div className="grid grid-cols-7 gap-px" role="grid" aria-label="Calendar">
        {days.map((d) => (
          <button key={`${d.y}-${d.m}-${d.d}`} type="button" role="gridcell" aria-selected={same(value, d)} aria-label={fmtDate(d)} onClick={() => onPick(d)}
            className={clsx('h-7 rounded-md text-[12px] tabular-nums',
              same(value, d) ? 'bg-accent font-semibold text-accent-fg' : 'hover:bg-hover',
              d.m !== view.m && !same(value, d) && 'text-faint',
              same(today, d) && !same(value, d) && 'font-semibold text-accent')}>
            {d.d}
          </button>
        ))}
      </div>
    </div>
  )
}

/** TimeFields: hours, minutes and seconds; arrow keys step them. */
function TimeFields({ value, onChange }: { value: { h: number; mi: number; s: number } | null; onChange: (t: { h: number; mi: number; s: number }) => void }) {
  const cur = value ?? { h: 0, mi: 0, s: 0 }
  const parts = [{ key: 'h', label: 'Hours', max: 23 }, { key: 'mi', label: 'Minutes', max: 59 }, { key: 's', label: 'Seconds', max: 59 }] as const
  return (
    <div className="flex items-center gap-0.5 font-mono text-[13px]">
      {parts.map((p, i) => (
        <span key={p.key} className="flex items-center gap-0.5">
          {i > 0 && <span className="text-muted">:</span>}
          <input aria-label={p.label} inputMode="numeric" maxLength={2} value={String(cur[p.key]).padStart(2, '0')}
            onFocus={(e) => e.target.select()}
            onChange={(e) => {
              const n = Number(e.target.value.replace(/\D/g, '').slice(-2))
              onChange({ ...cur, [p.key]: Math.min(p.max, n) })
            }}
            onKeyDown={(e) => {
              if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
              e.preventDefault()
              e.stopPropagation()
              const n = (cur[p.key] + (e.key === 'ArrowUp' ? 1 : -1) + p.max + 1) % (p.max + 1)
              onChange({ ...cur, [p.key]: n })
            }}
            className="h-7 w-9 rounded-md border border-line bg-panel-2 text-center focus:border-accent focus:outline-none" />
        </span>
      ))}
    </div>
  )
}
