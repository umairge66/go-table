import clsx from 'clsx'
import { ChevronDown, Database, Loader2, RefreshCw, X } from 'lucide-react'
import { createContext, forwardRef, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import { DRIVER_LOGOS } from '../lib/driverLogos'
import { driverInfo, tagInfo, type Driver, type Tag } from '../types'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'subtle'

export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant; size?: 'sm' | 'md'; icon?: ReactNode; loading?: boolean; kbd?: string
}>(function Button({ variant = 'secondary', size = 'md', icon, loading, kbd, className, children, disabled, ...rest }, ref) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={clsx(
        'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-lg font-medium transition-colors select-none',
        'disabled:opacity-45',
        size === 'sm' ? 'h-7 px-2.5 text-[12px]' : 'h-8 px-3 text-[13px]',
        variant === 'primary' && 'bg-accent text-accent-fg enabled:hover:brightness-110 shadow-sm',
        variant === 'secondary' && 'bg-panel border border-line text-fg enabled:hover:bg-hover shadow-[0_1px_0_rgb(0_0_0/0.03)]',
        variant === 'ghost' && 'text-fg-2 enabled:hover:bg-hover enabled:hover:text-fg',
        variant === 'subtle' && 'bg-hover text-fg enabled:hover:bg-active',
        variant === 'danger' && 'bg-err text-white enabled:hover:brightness-110',
        className,
      )}
      {...rest}
    >
      {loading ? <Loader2 className="size-3.5 animate-spin" /> : icon}
      {children}
      {kbd && <Kbd className="ml-1 opacity-70">{kbd}</Kbd>}
    </button>
  )
})

export function IconButton({ label, className, active, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; active?: boolean }) {
  return (
    <button
      type="button" // never submits a surrounding form
      aria-label={label}
      title={label}
      className={clsx('inline-flex size-7 items-center justify-center rounded-md text-muted transition-colors enabled:hover:bg-hover enabled:hover:text-fg disabled:opacity-40',
        active && 'bg-active text-accent', className)}
      {...rest}
    />
  )
}

const SPIN_MS = 600

/**
 * RefreshButton spins its icon while a refresh runs: at least one full
 * turn, stopping at the end of a turn. busy also spins it for refreshes
 * started elsewhere (e.g. Ctrl+R).
 */
export function RefreshButton({ label, onRefresh, busy = false, iconClassName = 'size-4' }: {
  label: string
  onRefresh: () => unknown
  busy?: boolean
  iconClassName?: string
}) {
  const [running, setRunning] = useState(false)
  const [spinning, setSpinning] = useState(false)
  const since = useRef(0)
  const startSpin = () => { if (!spinning) { since.current = performance.now(); setSpinning(true) } }
  const active = running || busy
  useEffect(() => {
    if (active) { startSpin(); return }
    if (!spinning) return
    const elapsed = performance.now() - since.current
    const t = setTimeout(() => setSpinning(false), Math.max(SPIN_MS, Math.ceil(elapsed / SPIN_MS) * SPIN_MS) - elapsed)
    return () => clearTimeout(t)
  }, [active, spinning]) // eslint-disable-line react-hooks/exhaustive-deps
  const click = async () => {
    startSpin()
    setRunning(true)
    try { await onRefresh() } finally { setRunning(false) }
  }
  return (
    <IconButton label={label} onClick={click} aria-busy={spinning || undefined}>
      <RefreshCw className={clsx(iconClassName, spinning && 'motion-safe:animate-[spin_0.6s_linear_infinite]')} />
    </IconButton>
  )
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { icon?: ReactNode; mono?: boolean }>(
  function Input({ icon, mono, className, ...rest }, ref) {
    return (
      <div className={clsx('relative flex items-center', className)}>
        {icon && <span className="pointer-events-none absolute left-2.5 text-faint [&>svg]:size-3.5">{icon}</span>}
        <input
          ref={ref}
          className={clsx('h-8 w-full rounded-lg border border-line bg-panel px-2.5 text-[13px] placeholder:text-faint',
            'transition-[border,box-shadow] focus:border-accent focus:shadow-[0_0_0_3px_color-mix(in_srgb,var(--accent)_18%,transparent)] focus:outline-none',
            icon && 'pl-8', mono && 'font-mono text-[12.5px]')}
          {...rest}
        />
      </div>
    )
  })

export function Select({ className, selectClassName, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement> & { selectClassName?: string }) {
  return (
    <div className={clsx('relative flex items-center', className)}>
      <select
        className={clsx('h-8 w-full appearance-none rounded-lg border border-line bg-panel pl-2.5 pr-7 text-[13px] focus:border-accent focus:outline-none', selectClassName)}
        {...rest}
      >
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2 size-3.5 text-muted" />
    </div>
  )
}

export function Field({ label, hint, children, className }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={clsx('flex flex-col gap-1', className)}>
      <span className="text-[11.5px] font-medium text-muted">{label}</span>
      {children}
      {hint && <span className="text-[11.5px] text-faint">{hint}</span>}
    </label>
  )
}

export function Switch({ checked, onChange, label, id }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode; id?: string }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-2.5 text-[13px]">
      <button
        id={id}
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={clsx('relative h-[18px] w-8 rounded-full transition-colors', checked ? 'bg-accent' : 'bg-line')}
      >
        <span className={clsx('absolute top-[2px] size-[14px] rounded-full bg-white shadow transition-[left]', checked ? 'left-[16px]' : 'left-[2px]')} />
      </button>
      {label}
    </label>
  )
}

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return <kbd className={clsx('rounded border border-line bg-panel-2 px-1 font-sans text-[10.5px] font-medium text-muted', className)}>{children}</kbd>
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={clsx('size-4 animate-spin text-muted', className)} />
}

/** DriverBadge is the engine's logo (white on its brand colour), or a generic database icon. */
export function DriverBadge({ driver, size = 32, className, decorative }: {
  driver: Driver; size?: number; className?: string
  /** decorative hides the badge from screen readers when the engine name is written next to it. */
  decorative?: boolean
}) {
  const d = driverInfo(driver)
  const logo = DRIVER_LOGOS[driver]
  const icon = Math.round(size * 0.6)
  return (
    <span
      role={decorative ? undefined : 'img'}
      aria-label={decorative ? undefined : d.label}
      aria-hidden={decorative || undefined}
      title={d.label}
      className={clsx('inline-flex shrink-0 items-center justify-center text-white', className)}
      style={{ width: size, height: size, borderRadius: size * 0.3, background: logo?.color ?? d.color }}
    >
      {logo
        ? <svg viewBox="0 0 24 24" width={icon} height={icon} fill="currentColor" aria-hidden="true"><path d={logo.path} /></svg>
        : <Database style={{ width: icon, height: icon }} aria-hidden="true" />}
    </span>
  )
}

export function TagPill({ tag, className }: { tag?: Tag; className?: string }) {
  if (!tag) return null
  const t = tagInfo(tag)
  return (
    <span
      className={clsx('inline-flex h-[18px] items-center rounded-full px-2 text-[10.5px] font-semibold uppercase tracking-wide', className)}
      style={{ color: t.color, background: `color-mix(in srgb, ${t.color} 14%, transparent)` }}
    >
      {t.label}
    </span>
  )
}

export function EmptyState({ icon, title, children, actions }: { icon: ReactNode; title: string; children?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center animate-fade">
      <div className="flex size-14 items-center justify-center rounded-2xl bg-accent-soft text-accent [&>svg]:size-7">{icon}</div>
      <div className="text-[15px] font-semibold">{title}</div>
      {children && <div className="max-w-md text-[13px] leading-relaxed text-muted">{children}</div>}
      {actions && <div className="mt-2 flex gap-2">{actions}</div>}
    </div>
  )
}

// ---------------------------------------------------------------- Modal

export function Modal({ title, label, subtitle, onClose, children, footer, width = 520 }: {
  title: ReactNode; label?: string; subtitle?: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; width?: number
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose])
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/30 pt-[12vh]" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-label={label ?? (typeof title === 'string' ? title : undefined)}
        className="flex max-h-[76vh] flex-col overflow-hidden rounded-xl border border-line bg-panel shadow-pop animate-pop"
        style={{ width }}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4 px-5 pt-4 pb-3">
          <div>
            <div className="text-[15px] font-semibold">{title}</div>
            {subtitle && <div className="mt-0.5 text-[12.5px] text-muted">{subtitle}</div>}
          </div>
          <IconButton label="Close" onClick={onClose}><X className="size-4" /></IconButton>
        </div>
        <div className="min-h-0 flex-1 overflow-auto px-5 pb-4">{children}</div>
        {footer && <div className="flex items-center justify-end gap-2 border-t border-line-2 bg-panel-2 px-5 py-3">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

// ---------------------------------------------------------------- Menu

export interface MenuItem { label: string; icon?: ReactNode; kbd?: string; danger?: boolean; disabled?: boolean; onSelect: () => void }
export type MenuEntry = MenuItem | 'sep'

export function Menu({ x, y, items, onClose }: { x: number; y: number; items: MenuEntry[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ x, y })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    setPos({ x: Math.min(x, window.innerWidth - r.width - 8), y: Math.min(y, window.innerHeight - r.height - 8) })
  }, [x, y])
  useEffect(() => {
    const close = () => onClose()
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('mousedown', close)
    window.addEventListener('blur', close)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', close)
      window.removeEventListener('blur', close)
      window.removeEventListener('keydown', onKey)
    }
  }, [onClose])
  return createPortal(
    <div
      ref={ref}
      role="menu"
      className="fixed z-[60] min-w-[200px] rounded-lg border border-line bg-panel p-1 shadow-pop animate-pop"
      style={{ left: pos.x, top: pos.y }}
      onMouseDown={(e) => e.stopPropagation()}
    >
      {items.map((it, i) =>
        it === 'sep' ? <div key={i} className="my-1 h-px bg-line-2" /> : (
          <button
            key={i}
            role="menuitem"
            disabled={it.disabled}
            onClick={() => { onClose(); it.onSelect() }}
            className={clsx('flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12.5px] enabled:hover:bg-accent enabled:hover:text-accent-fg disabled:opacity-40',
              it.danger && 'text-err')}
          >
            <span className="flex w-4 justify-center [&>svg]:size-3.5">{it.icon}</span>
            <span className="flex-1">{it.label}</span>
            {it.kbd && <span className="text-[11px] opacity-60">{it.kbd}</span>}
          </button>
        ),
      )}
    </div>,
    document.body,
  )
}

export function useMenu() {
  const [menu, setMenu] = useState<{ x: number; y: number; items: MenuEntry[] } | null>(null)
  const open = useCallback((e: { clientX: number; clientY: number; preventDefault?: () => void }, items: MenuEntry[]) => {
    e.preventDefault?.()
    setMenu({ x: e.clientX, y: e.clientY, items })
  }, [])
  const node = menu ? <Menu {...menu} onClose={() => setMenu(null)} /> : null
  return { open, node }
}

// ---------------------------------------------------------------- Toasts

type ToastKind = 'ok' | 'error' | 'info'
interface ToastT { id: number; kind: ToastKind; title: string; body?: string }

const ToastCtx = createContext<(kind: ToastKind, title: string, body?: string) => void>(() => {})

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastT[]>([])
  const push = useCallback((kind: ToastKind, title: string, body?: string) => {
    const id = Date.now() + Math.random()
    setToasts((t) => [...t.slice(-3), { id, kind, title, body }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), kind === 'error' ? 6000 : 3500)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      {/* Above the bottom toolbars and status bar, so toasts never cover paging or commit buttons. */}
      <div className="pointer-events-none fixed bottom-[84px] right-4 z-[70] flex w-[360px] flex-col gap-2">
        {toasts.map((t) => (
          <div key={t.id} role="status" className="pointer-events-auto flex gap-3 rounded-xl border border-line bg-panel p-3 shadow-pop animate-slide">
            <span className={clsx('mt-1 size-2 shrink-0 rounded-full', t.kind === 'ok' ? 'bg-ok' : t.kind === 'error' ? 'bg-err' : 'bg-accent')} />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium">{t.title}</div>
              {t.body && <div className="mt-0.5 break-words text-[12px] text-muted">{t.body}</div>}
            </div>
            <button className="text-faint hover:text-fg" onClick={() => setToasts((x) => x.filter((y) => y.id !== t.id))}><X className="size-3.5" /></button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

export const useToast = () => useContext(ToastCtx)

/** errorText turns an ApiError or any error into toast title/body. */
export function errorParts(e: unknown): [string, string] {
  const err = e as { title?: string; hint?: string; detail?: string; message?: string }
  if (err?.title) return [err.title, [err.hint, err.detail].filter(Boolean).join(' — ')]
  return ['Something went wrong', String(err?.message ?? e)]
}

// ---------------------------------------------------------------- Split

/** useResizable returns a size and a mouse-down handler for a drag handle. */
export function useResizable(initial: number, min: number, max: number, axis: 'x' | 'y' = 'x', invert = false, storageKey?: string) {
  const [size, setSize] = useState(() => {
    const saved = storageKey ? Number(localStorage.getItem(storageKey)) : 0
    return saved > 0 ? saved : initial
  })
  const onMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const start = axis === 'x' ? e.clientX : e.clientY
    const startSize = size
    const move = (ev: MouseEvent) => {
      const delta = (axis === 'x' ? ev.clientX : ev.clientY) - start
      const next = Math.max(min, Math.min(max, startSize + (invert ? -delta : delta)))
      setSize(next)
      if (storageKey) localStorage.setItem(storageKey, String(next))
    }
    const up = () => {
      window.removeEventListener('mousemove', move)
      window.removeEventListener('mouseup', up)
      document.body.style.cursor = ''
    }
    document.body.style.cursor = axis === 'x' ? 'col-resize' : 'row-resize'
    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', up)
  }, [axis, invert, max, min, size, storageKey])
  return [size, onMouseDown] as const
}

export function DragHandle({ axis = 'x', onMouseDown }: { axis?: 'x' | 'y'; onMouseDown: (e: React.MouseEvent) => void }) {
  return (
    <div
      onMouseDown={onMouseDown}
      className={clsx('group relative z-10 shrink-0', axis === 'x' ? 'w-px cursor-col-resize bg-line' : 'h-px cursor-row-resize bg-line')}
    >
      <div className={clsx('absolute transition-colors group-hover:bg-accent/60', axis === 'x' ? '-inset-x-1 inset-y-0' : '-inset-y-1 inset-x-0')} />
    </div>
  )
}
