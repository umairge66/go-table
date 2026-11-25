import clsx from 'clsx'
import { Check, Search } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { DriverBadge, Input } from '../components/ui'
import { DRIVERS, ENGINES, driverInfo, family, type Driver } from '../types'
import { dragWindow, useOwnTitleBar } from '../components/WindowChrome'

/** useUnsupported tells which engines this build of GoTable lacks (DuckDB, without CGO). */
export function useUnsupported() {
  const [supported, setSupported] = useState<string[] | null>(null)
  useEffect(() => { api.drivers().then((r) => setSupported(r.supported)).catch(() => setSupported(null)) }, [])
  return (d: Driver) => !!supported && !supported.includes(d)
}

/** GROUPS: the engines, then what is compatible with each of them. */
const GROUPS: { title: string; drivers: Driver[] }[] = [
  { title: 'Databases', drivers: ENGINES },
  ...ENGINES.filter((e, i) => ENGINES.findIndex((x) => family(x) === family(e)) === i).map((e) => ({
    title: `${driverInfo(e).label} compatible`,
    drivers: DRIVERS.filter((d) => d.family === family(e) && !ENGINES.includes(d.id)).map((d) => d.id),
  })),
].filter((g) => g.drivers.length > 0)

/** engineOf is the engine a compatible server works like. */
const engineOf = (d: Driver) => driverInfo(ENGINES.find((e) => family(e) === family(d))!)

/**
 * DriverPicker lists every database GoTable connects to. It is one Tab stop:
 * the arrow keys move through the list, and a search narrows it (also by
 * engine: "postgres" finds what is compatible with PostgreSQL too).
 */
export function DriverPicker({ value, onChange, className }: { value: Driver; onChange: (d: Driver) => void; className?: string }) {
  const ownTitleBar = useOwnTitleBar()
  const unsupported = useUnsupported()
  const [search, setSearch] = useState('')
  const list = useRef<HTMLDivElement>(null)
  const groups = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return GROUPS
    const has = (s: string) => s.toLowerCase().includes(q)
    return GROUPS
      .map((g) => ({ ...g, drivers: g.drivers.filter((d) => has(driverInfo(d).label) || has(d) || has(engineOf(d).label)) }))
      .filter((g) => g.drivers.length > 0)
  }, [search])
  const flat = groups.flatMap((g) => g.drivers).filter((d) => !unsupported(d))
  const tabStop = flat.includes(value) ? value : flat[0]

  const radio = (d: Driver) => list.current?.querySelector<HTMLElement>(`[data-driver="${d}"]`)
  useEffect(() => { radio(value)?.scrollIntoView({ block: 'nearest' }) }, [value])

  const pick = (d: Driver | undefined) => {
    if (!d) return
    onChange(d)
    radio(d)?.focus()
  }
  const onListKey = (e: React.KeyboardEvent) => {
    const step = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const from = flat.indexOf((e.target as HTMLElement).dataset.driver as Driver)
    pick(flat[Math.max(0, Math.min(flat.length - 1, from + step))])
  }

  return (
    <div className={clsx('flex min-h-0 shrink-0 flex-col border-r border-line bg-panel-2', className)}>
      <div className="shrink-0 px-3 pb-2 pt-4" onMouseDown={ownTitleBar ? dragWindow : undefined}>
        <div className="mb-2.5 flex items-baseline justify-between px-1">
          <span className="text-[13px] font-semibold">Connect to</span>
          <span className="text-[11.5px] tabular-nums text-muted">{DRIVERS.length} supported</span>
        </div>
        <Input type="search" icon={<Search />} value={search} placeholder="Search, e.g. Neon" aria-label="Search the types" autoComplete="off" spellCheck={false}
          onChange={(e) => setSearch(e.target.value)}
          onKeyDown={(e) => {
            // Enter picks the first match rather than submitting the form.
            if (e.key === 'Enter') { e.preventDefault(); if (search.trim()) pick(flat[0]) }
            else if (e.key === 'ArrowDown') { e.preventDefault(); pick(search.trim() ? flat[0] : tabStop) }
            else if (e.key === 'Escape' && search) { e.preventDefault(); e.stopPropagation(); setSearch('') }
          }} />
      </div>
      <div ref={list} role="radiogroup" aria-label="Database type" className="min-h-0 flex-1 overflow-y-auto px-2 pb-3" onKeyDown={onListKey}>
        {groups.map((g) => (
          <div key={g.title} role="presentation">
            <div className="px-2 pb-1 pt-3 text-[11px] font-medium text-muted">{g.title}</div>
            {g.drivers.map((d) => {
              const info = driverInfo(d)
              const on = value === d
              const missing = unsupported(d)
              return (
                <button
                  key={d}
                  type="button"
                  role="radio"
                  data-driver={d}
                  aria-checked={on}
                  tabIndex={tabStop === d ? 0 : -1}
                  disabled={missing}
                  title={missing ? `${info.label} isn't available in this build of GoTable`
                    : ENGINES.includes(d) ? undefined : `${info.label}: works like ${engineOf(d).label}${info.hosted ? ', over TLS' : ''}`}
                  onClick={() => onChange(d)}
                  className={clsx('flex h-9 w-full scroll-my-2 items-center gap-2.5 rounded-lg px-2 text-left text-[13px] transition-colors disabled:opacity-40',
                    on ? 'bg-active font-medium text-fg' : 'text-fg-2 enabled:hover:bg-hover enabled:hover:text-fg')}
                >
                  <DriverBadge driver={d} size={24} decorative />
                  <span className="min-w-0 flex-1 truncate">{info.label}</span>
                  {on && <Check className="size-4 shrink-0 text-accent" aria-hidden="true" />}
                </button>
              )
            })}
          </div>
        ))}
        {groups.length === 0 && <div className="px-3 py-8 text-center text-[12.5px] text-muted">Nothing matches “{search}”.</div>}
      </div>
    </div>
  )
}
