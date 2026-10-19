import clsx from 'clsx'
import { FileUp, HardDrive, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { nativeDialogs, openFileDialog } from '../lib/native'
import { examplePath } from '../lib/platform'
import { fmtBytes } from './Progress'
import { IconButton, Input } from './ui'

export type PickedFile = { kind: 'file'; file: File } | { kind: 'path'; path: string }

export const pickedName = (p: PickedFile) => (p.kind === 'file' ? p.file.name : p.path.split(/[\\/]/).pop() || p.path)

/** FilePicker accepts a dropped/chosen file or a path on this computer. */
export function FilePicker({ accept, hint, value, onChange }: {
  accept: string
  hint: string
  value: PickedFile | null
  onChange: (p: PickedFile | null) => void
}) {
  const input = useRef<HTMLInputElement>(null)
  const [over, setOver] = useState(false)
  const [usePath, setUsePath] = useState(false)
  const [path, setPath] = useState('')
  // In the desktop window the system "Open" dialog picks the file, and the
  // import reads it in place (no copy, any size).
  const [native, setNative] = useState(false)
  useEffect(() => { nativeDialogs().then(setNative) }, [])
  const choose = () => {
    if (!native) { input.current?.click(); return }
    const kinds = accept.split(',').map((x) => x.trim().replace(/^\./, '')).filter(Boolean)
    openFileDialog(kinds).then((f) => { if (f) onChange({ kind: 'path', path: f.path }) }).catch(() => input.current?.click())
  }

  if (value) {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-line bg-panel-2 p-3" data-testid="picked-file">
        <div className="flex size-9 items-center justify-center rounded-lg bg-accent-soft text-accent"><FileUp className="size-4" /></div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium">{pickedName(value)}</div>
          <div className="truncate text-[12px] text-muted">{value.kind === 'file' ? fmtBytes(value.file.size) : value.path}</div>
        </div>
        <IconButton label="Choose another file" onClick={() => onChange(null)}><X className="size-4" /></IconButton>
      </div>
    )
  }
  return (
    <div className="space-y-2">
      <div
        role="button"
        tabIndex={0}
        aria-label="Choose a file"
        onClick={choose}
        onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && choose()}
        onDragOver={(e) => { e.preventDefault(); setOver(true) }}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => {
          e.preventDefault()
          setOver(false)
          const f = e.dataTransfer.files[0]
          if (f) onChange({ kind: 'file', file: f })
        }}
        className={clsx('flex flex-col items-center justify-center gap-1.5 rounded-xl border-2 border-dashed px-4 py-7 text-center transition-colors',
          over ? 'border-accent bg-accent-soft' : 'border-line hover:border-faint hover:bg-hover')}
      >
        <FileUp className="size-6 text-accent" />
        <div className="text-[13px] font-medium">Drop a file here, or click to choose</div>
        <div className="text-[12px] text-muted">{hint}</div>
        <input ref={input} type="file" accept={accept} hidden data-testid="file-input"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) onChange({ kind: 'file', file: f }); e.target.value = '' }} />
      </div>
      {native ? null : usePath ? (
        <div className="flex gap-2">
          <Input mono icon={<HardDrive />} className="flex-1" autoFocus value={path} placeholder={examplePath('backups/dump.sql')} aria-label="File path"
            onChange={(e) => setPath(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && path.trim() && onChange({ kind: 'path', path: path.trim() })} />
          <button className="text-[12.5px] font-medium text-accent hover:underline disabled:opacity-40" disabled={!path.trim()}
            onClick={() => onChange({ kind: 'path', path: path.trim() })}>Use</button>
        </div>
      ) : (
        <button className="text-[12px] text-muted hover:text-accent" onClick={() => setUsePath(true)}>
          Very large file? Use its path on this computer instead (no copy needed)
        </button>
      )}
    </div>
  )
}
