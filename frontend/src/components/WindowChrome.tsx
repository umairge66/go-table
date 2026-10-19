import clsx from 'clsx'
import { Copy, Minus, Square, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { nativeInfo, windowAction } from '../lib/native'

/** useOwnTitleBar is true when the page draws the window's title bar. */
export function useOwnTitleBar() {
  const [own, setOwn] = useState(false)
  useEffect(() => { nativeInfo().then((i) => setOwn(i.window)) }, [])
  return own
}

function useMaximized() {
  const check = () => window.outerWidth >= screen.availWidth - 1 && window.outerHeight >= screen.availHeight - 1
  const [max, setMax] = useState(check)
  useEffect(() => {
    const on = () => setMax(check())
    window.addEventListener('resize', on)
    return () => window.removeEventListener('resize', on)
  }, [])
  return max
}

const INTERACTIVE = 'button, a, input, select, textarea, label, [role=button], [role=tab], [role=option], [contenteditable=true], [data-no-drag]'

/**
 * dragWindow, used as onMouseDown of a title-bar area: dragging empty space
 * moves the window and double-clicking maximizes or restores it. Clicks on
 * controls inside the bar work as usual.
 */
export function dragWindow(e: React.MouseEvent) {
  if (e.button !== 0 || (e.target as HTMLElement).closest(INTERACTIVE)) return
  e.preventDefault()
  windowAction(e.detail === 2 ? 'toggle-maximize' : 'drag')
}

/** WindowControls are the minimize, maximize/restore and close buttons. */
export function WindowControls({ className }: { className?: string }) {
  const max = useMaximized()
  const btn = 'flex size-8 items-center justify-center rounded-md text-muted transition-colors hover:bg-hover hover:text-fg'
  return (
    <div className={clsx('flex items-center gap-0.5', className)} data-no-drag>
      <button type="button" className={btn} aria-label="Minimize" title="Minimize" onClick={() => windowAction('minimize')}><Minus className="size-4" /></button>
      <button type="button" className={btn} aria-label={max ? 'Restore' : 'Maximize'} title={max ? 'Restore' : 'Maximize'} onClick={() => windowAction('toggle-maximize')}>
        {max ? <Copy className="size-3.5 -scale-x-100" /> : <Square className="size-3.5" />}
      </button>
      <button type="button" className={clsx(btn, 'hover:!bg-[#e5484d] hover:!text-white')} aria-label="Close" title="Close" onClick={() => windowAction('close')}><X className="size-4" /></button>
    </div>
  )
}

const EDGES: { edge: string; cls: string; cursor: string }[] = [
  { edge: 'n', cls: 'top-0 left-2 right-2 h-1', cursor: 'ns-resize' },
  { edge: 's', cls: 'bottom-0 left-2 right-2 h-1', cursor: 'ns-resize' },
  { edge: 'w', cls: 'left-0 top-2 bottom-2 w-1', cursor: 'ew-resize' },
  { edge: 'e', cls: 'right-0 top-2 bottom-2 w-1', cursor: 'ew-resize' },
  { edge: 'nw', cls: 'top-0 left-0 size-2', cursor: 'nwse-resize' },
  { edge: 'se', cls: 'bottom-0 right-0 size-2', cursor: 'nwse-resize' },
  { edge: 'ne', cls: 'top-0 right-0 size-2', cursor: 'nesw-resize' },
  { edge: 'sw', cls: 'bottom-0 left-0 size-2', cursor: 'nesw-resize' },
]

/** ResizeEdges give a frameless window its resize borders (not when maximized). */
export function ResizeEdges() {
  const max = useMaximized()
  if (max) return null
  return (
    <>
      {EDGES.map((e) => (
        <div key={e.edge} aria-hidden className={clsx('fixed z-[100]', e.cls)} style={{ cursor: e.cursor }}
          onMouseDown={(ev) => { if (ev.button === 0) { ev.preventDefault(); windowAction('resize-' + e.edge) } }} />
      ))}
    </>
  )
}
