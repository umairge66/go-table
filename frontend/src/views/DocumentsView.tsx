import { useVirtualizer } from '@tanstack/react-virtual'
import clsx from 'clsx'
import { Braces, ChevronRight, Copy, Files, Lock, Pencil, Plus, Trash2, Undo2 } from 'lucide-react'
import { memo, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { JsonEditor } from '../components/JsonEditor'
import { Button, IconButton, useMenu, useToast, type MenuEntry } from '../components/ui'
import type { ShownDoc } from '../lib/docEdits'
import { compactJSON, conversions, formatJSON, getAt, parseJSON, pretty, renameAt, typeOf, updateAt, type JNode, type JPath, type JType } from '../lib/json'
import { isDynamo, isMongo, type Driver } from '../types'

export type DocView = 'json' | 'tree'

interface Props {
  docs: ShownDoc[]
  driver: Driver
  readOnly: boolean
  offset: number
  /** editing: the document (by key) whose JSON editor is open. */
  editing: string | null
  setEditing: (key: string | null) => void
  onChange: (i: number, json: string) => void
  onDelete: (i: number) => void
  onUndo: (i: number) => void
  onDuplicate?: (i: number) => void
  /** onOpen opens a Redis key by itself. */
  onOpen?: (d: ShownDoc) => void
}

/** DocumentsView shows documents whole: as JSON, or as trees. */
export function DocumentsView({ view, ...p }: Props & { view: DocView }) {
  return view === 'json' ? <JsonList {...p} /> : <TreeList {...p} />
}

// ---------------------------------------------------------------- JSON

const COLLAPSED = 22 // lines shown of a long document

function JsonList(p: Props) {
  const scroll = useRef<HTMLDivElement>(null)
  const virt = useVirtualizer({ count: p.docs.length, getScrollElement: () => scroll.current, estimateSize: () => 240, overscan: 4, getItemKey: (i) => p.docs[i].key })
  // A new document's editor comes into view.
  useEffect(() => {
    const i = p.editing ? p.docs.findIndex((d) => d.key === p.editing) : -1
    if (i >= 0 && p.docs[i].status === 'inserted') virt.scrollToIndex(i, { align: 'start' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.editing])
  return (
    <div ref={scroll} className="h-full overflow-y-auto bg-bg px-4 py-2" data-testid="json-view">
      <div className="relative w-full" style={{ height: virt.getTotalSize() }}>
        {virt.getVirtualItems().map((it) => (
          <div key={it.key} ref={virt.measureElement} data-index={it.index} className="absolute inset-x-0 top-0 py-1.5" style={{ transform: `translateY(${it.start}px)` }}>
            <JsonCard p={p} i={it.index} d={p.docs[it.index]} />
          </div>
        ))}
      </div>
    </div>
  )
}

function JsonCard({ p, i, d }: { p: Props; i: number; d: ShownDoc }) {
  const toast = useToast()
  const [all, setAll] = useState(false)
  const text = useMemo(() => pretty(d.json), [d.json])
  const lines = useMemo(() => text.split('\n'), [text])
  const open = p.editing === d.key
  const [draft, setDraft] = useState(text)
  const [problem, setProblem] = useState('')
  useEffect(() => { if (open) { setDraft(text); setProblem('') } }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  const locked = p.readOnly || !!d.readOnly || d.status === 'deleted'
  const apply = () => {
    try {
      const n = parseJSON(draft)
      // Formatting alone isn't a change.
      const same = compactJSON(n) === compactOf(d.json)
      if (!same) p.onChange(i, d.status === 'inserted' ? draft : compactJSON(n))
      else if (d.status === 'inserted') p.onChange(i, draft)
      p.setEditing(null)
    } catch (e) { setProblem((e as Error).message) }
  }
  const change = (t: string) => {
    setDraft(t)
    try { parseJSON(t); setProblem('') } catch (e) { setProblem((e as Error).message) }
  }
  const cancel = () => { p.setEditing(null); if (d.status === 'inserted' && !d.json.trim()) p.onDelete(i) }
  const shown = all || lines.length <= COLLAPSED + 4 ? text : lines.slice(0, COLLAPSED).join('\n')
  return (
    <article aria-label={`Document ${d.label}`} data-testid="json-doc" data-status={d.status}
      className={clsx('overflow-hidden rounded-xl border bg-panel', open ? 'border-accent' : 'border-line', d.status === 'deleted' && 'opacity-60')}>
      <DocHeader p={p} i={i} d={d}>
        <IconButton label="Copy" onClick={() => navigator.clipboard?.writeText(text).then(() => toast('ok', 'Copied', d.label))}><Copy className="size-3.5" /></IconButton>
        {!open && !locked && <IconButton label="Edit" onClick={() => p.setEditing(d.key)}><Pencil className="size-3.5" /></IconButton>}
      </DocHeader>
      {open ? (
        <>
          <JsonEditor value={draft} label={`JSON of ${d.label}`} onChange={change} onSave={apply} onCancel={cancel} />
          <div className="flex min-h-11 items-center gap-2 border-t border-line bg-panel-2 px-3 py-1.5">
            <span className={clsx('min-w-0 flex-1 truncate text-[12px]', problem ? 'text-err' : 'text-muted')} role={problem ? 'alert' : undefined}>{problem || 'Nothing is written until you commit.'}</span>
            <Button size="sm" variant="ghost" icon={<Braces className="size-3.5" />} disabled={!!problem} onClick={() => setDraft(pretty(draft))}>Format</Button>
            <Button size="sm" variant="ghost" onClick={cancel}>Cancel</Button>
            <Button size="sm" variant="primary" disabled={!!problem} onClick={apply} kbd="Ctrl+↵">Apply</Button>
          </div>
        </>
      ) : (
        <div onDoubleClick={() => { if (!locked && !window.getSelection()?.toString().includes('\n')) p.setEditing(d.key) }}>
          <pre className={clsx('select-text overflow-x-auto whitespace-pre-wrap break-words px-4 py-2.5 font-mono text-[12.5px] leading-[1.55]', d.status === 'edited' && 'bg-edited/50', d.status === 'inserted' && 'bg-inserted/50', d.status === 'deleted' && 'bg-deleted/50')}>
            <Highlighted text={shown} />
          </pre>
          {shown !== text || all ? (
            <button className="w-full border-t border-line-2 py-1 text-[12px] font-medium text-accent hover:bg-hover" onClick={() => setAll(!all)}>{all ? 'Show less' : `Show all ${lines.length} lines`}</button>
          ) : null}
        </div>
      )}
    </article>
  )
}

const compactOf = (s: string) => { try { return compactJSON(parseJSON(s)) } catch { return s } }

function DocHeader({ p, i, d, children }: { p: Props; i: number; d: ShownDoc; children?: ReactNode }) {
  return (
    <div className="flex h-9 items-center gap-2 border-b border-line-2 bg-panel-2 pl-3 pr-1.5">
      <span className="text-[11.5px] tabular-nums text-faint">{d.status === 'inserted' ? '+' : p.offset + i + 1}</span>
      {p.onOpen && d.status !== 'inserted'
        ? <button className="min-w-0 truncate font-mono text-[12px] font-medium text-accent hover:underline" title="Open the key" onClick={() => p.onOpen!(d)}>{d.label}</button>
        : <span className="min-w-0 select-text truncate font-mono text-[12px] font-medium text-fg-2" title={d.label}>{d.label}</span>}
      {d.meta && <span className="shrink-0 text-[11.5px] text-muted">{d.meta}</span>}
      {d.readOnly && <span className="flex min-w-0 items-center gap-1 text-[11.5px] text-muted" title={d.readOnly}><Lock className="size-3 shrink-0" /><span className="truncate">{d.readOnly}</span></span>}
      <Status d={d} />
      <div className="flex-1" />
      {children}
      {!p.readOnly && p.onDuplicate && d.status !== 'deleted' && !d.readOnly && <IconButton label="Duplicate" onClick={() => p.onDuplicate!(i)}><Files className="size-3.5" /></IconButton>}
      {d.status === 'edited' && <IconButton label="Undo the changes" onClick={() => p.onUndo(i)}><Undo2 className="size-3.5" /></IconButton>}
      {!p.readOnly && (d.status === 'deleted'
        ? <IconButton label="Keep the document" onClick={() => p.onDelete(i)}><Undo2 className="size-3.5" /></IconButton>
        : <IconButton label="Delete document" className="enabled:hover:!text-err" onClick={() => p.onDelete(i)}><Trash2 className="size-3.5" /></IconButton>)}
    </div>
  )
}

function Status({ d }: { d: ShownDoc }) {
  if (d.status === 'clean') return null
  const [text, cls] = d.status === 'edited' ? ['edited', 'bg-warn/15 text-warn'] : d.status === 'deleted' ? ['will be deleted', 'bg-err/15 text-err'] : ['new', 'bg-ok/15 text-ok']
  return <span className={clsx('shrink-0 rounded px-1.5 py-0.5 text-[11px] font-medium', cls)}>{text}</span>
}

const TOKEN = /("(?:[^"\\]|\\.)*")(\s*:)?|\b(true|false|null)\b|(-?\d[\d.eE+-]*)/g

/** Highlighted colours formatted JSON. */
const Highlighted = memo(function Highlighted({ text }: { text: string }) {
  const out: ReactNode[] = []
  let at = 0
  for (const m of text.matchAll(TOKEN)) {
    if (m.index > at) out.push(text.slice(at, m.index))
    if (m[2] !== undefined) out.push(<span key={m.index} className="text-accent">{m[1]}</span>, m[2])
    else out.push(<span key={m.index} className={m[1] ? 'text-ok' : 'text-warn'}>{m[0]}</span>)
    at = m.index + m[0].length
  }
  out.push(text.slice(at))
  return <>{out}</>
})

// ---------------------------------------------------------------- tree

interface Row {
  id: string
  doc: number
  depth: number
  /** path: from the document's root; empty for the document itself. */
  path: JPath
  key: string
  node?: JNode
  type?: JType
  /** inList: its name is a position, which can't be changed. */
  inList: boolean
  open: boolean
  count: number
}

const ROW = 27
const rowId = (doc: string, path: JPath) => doc + '\u0000' + path.join('\u0000')

function TreeList(p: Props) {
  const scroll = useRef<HTMLDivElement>(null)
  const menu = useMenu()
  const toast = useToast()
  const dialect = isMongo(p.driver) ? 'mongo' : isDynamo(p.driver) ? 'dynamo' : 'plain'
  // Documents start open, what is nested in them closed.
  const [toggled, setToggled] = useState<Set<string>>(new Set())
  const [sel, setSel] = useState<string | null>(null)
  const [edit, setEdit] = useState<{ id: string; what: 'key' | 'value' } | null>(null)
  const parsed = useRef(new Map<string, JNode | null>())
  const rootOf = (json: string) => {
    let n = parsed.current.get(json)
    if (n === undefined) {
      try { n = parseJSON(json) } catch { n = null }
      if (parsed.current.size > 12000) parsed.current.clear()
      parsed.current.set(json, n)
    }
    return n
  }
  const [everything, setEverything] = useState(false)
  const rows = useMemo(() => {
    const out: Row[] = []
    p.docs.forEach((d, doc) => {
      const root = rootOf(d.json)
      const id = rowId(d.key, [])
      const open = !toggled.has(id)
      const type = root ? typeOf(root) : undefined
      out.push({ id, doc, depth: 0, path: [], key: d.label, node: root ?? undefined, type, inList: false, open, count: type?.items?.length ?? 0 })
      if (!open || !root || !type) return
      if (!type.items) { // a value that isn't an object: a Redis text
        out.push({ id: id + '\u0000value', doc, depth: 1, path: [], key: 'value', node: root, type, inList: true, open: false, count: 0 })
        return
      }
      const walk = (t: JType, path: JPath, depth: number) => {
        for (const c of t.items ?? []) {
          const cp = [...path, ...c.path]
          const ct = typeOf(c.node)
          const cid = rowId(d.key, cp)
          const isOpen = !!ct.items && (everything !== toggled.has(cid))
          out.push({ id: cid, doc, depth, path: cp, key: c.key, node: c.node, type: ct, inList: !!t.array, open: isOpen, count: ct.items?.length ?? 0 })
          if (isOpen) walk(ct, cp, depth + 1)
        }
      }
      walk(type, [], 1)
    })
    return out
  }, [p.docs, toggled, everything]) // eslint-disable-line react-hooks/exhaustive-deps
  const virt = useVirtualizer({ count: rows.length, getScrollElement: () => scroll.current, estimateSize: () => ROW, overscan: 20, getItemKey: (i) => rows[i].id })
  const toggle = (id: string) => setToggled((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n })
  const locked = (r: Row) => { const d = p.docs[r.doc]; return p.readOnly || !!d.readOnly || d.status === 'deleted' }
  const write = (r: Row, fn: (root: JNode) => JNode) => {
    const root = rootOf(p.docs[r.doc].json)
    if (!root) return
    const next = compactJSON(fn(root))
    if (next !== compactOf(p.docs[r.doc].json)) p.onChange(r.doc, next)
  }
  const setValue = (r: Row, n: JNode) => write(r, (root) => updateAt(root, r.path, () => n))
  const remove = (r: Row) => { if (!r.depth) p.onDelete(r.doc); else if (r.path.length) write(r, (root) => updateAt(root, r.path, () => undefined)) }
  const containerPath = (r: Row) => { // where a container's children are (a set's are inside its wrapper)
    if (r.node?.t === 'object' && r.type?.array) return [...r.path, r.node.e[0][0]]
    return r.path
  }
  const add = (r: Row) => {
    const path = containerPath(r)
    let newPath: JPath = path
    write(r, (root) => updateAt(root, path, (c) => {
      if (c.t === 'array') {
        newPath = [...path, c.v.length]
        // A list's new item is like its others.
        const last = c.v[c.v.length - 1]
        return { t: 'array', v: [...c.v, last?.t === 'number' ? { t: 'number', raw: '0' } : { t: 'string', v: '' }] }
      }
      if (c.t !== 'object') return c
      let name = 'field'
      for (let k = 2; c.e.some(([x]) => x === name); k++) name = `field${k}`
      newPath = [...path, name]
      return { t: 'object', e: [...c.e, [name, { t: 'string', v: '' }]] }
    }))
    if (!r.open) toggle(r.id)
    const id = rowId(p.docs[r.doc].key, newPath)
    setSel(id)
    setEdit({ id, what: r.type?.array ? 'value' : 'key' })
  }
  const rename = (r: Row, name: string) => {
    setEdit(null)
    if (name === r.key) return
    const parent = getAt(rootOf(p.docs[r.doc].json)!, r.path.slice(0, -1))
    if (!name || (parent?.t === 'object' && parent.e.some(([k]) => k === name))) { toast('error', name ? `There is already a field “${name}”` : 'A field needs a name'); return }
    write(r, (root) => renameAt(root, r.path, name))
    const id = rowId(p.docs[r.doc].key, [...r.path.slice(0, -1), name])
    setSel(id)
    if (r.open) toggle(id)
  }
  const isId = (r: Row) => r.path.length === 1 && r.path[0] === '_id' && isMongo(p.driver)
  const rowMenu = (e: React.MouseEvent, r: Row) => {
    setSel(r.id)
    const d = p.docs[r.doc]
    const editable = !locked(r)
    const text = r.node ? (r.type?.text ?? compactJSON(r.node)) : d.json
    const items: MenuEntry[] = [
      { label: 'Copy value', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(r.node && r.type?.items ? formatJSON(r.node) : text) },
      ...(r.path.length ? [{ label: 'Copy path', onSelect: () => navigator.clipboard?.writeText(r.path.filter((x) => typeof x === 'number' || !/^\$(ss|ns|bs)$/.test(x)).join('.')) }] : []),
    ]
    if (editable && r.path.length && !isId(r)) {
      items.push('sep')
      if (r.type?.read) items.push({ label: 'Edit value', icon: <Pencil />, kbd: '↵', onSelect: () => setEdit({ id: r.id, what: 'value' }) })
      if (!r.inList) items.push({ label: 'Rename', kbd: 'F2', onSelect: () => setEdit({ id: r.id, what: 'key' }) })
      for (const c of conversions(dialect)) {
        if (c.label !== r.type?.label) items.push({ label: `Change to ${c.label}`, onSelect: () => { setValue(r, c.make(r.type?.text ?? '')); if (c.label !== 'Null' && c.label !== 'Object' && c.label !== 'Array') setEdit({ id: r.id, what: 'value' }) } })
      }
    }
    if (editable && r.type?.items) items.push('sep', { label: r.type.array ? 'Add an item' : 'Add a field', icon: <Plus />, onSelect: () => add(r) })
    if (!p.readOnly && d.status !== 'deleted' && (!r.path.length || (editable && !isId(r)))) {
      items.push('sep', { label: r.path.length ? (r.inList ? 'Remove item' : 'Remove field') : 'Delete document', icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => remove(r) })
    }
    menu.open(e, items)
  }
  const onKey = (e: React.KeyboardEvent) => {
    if (edit) return
    const i = rows.findIndex((r) => r.id === sel)
    const r = rows[i]
    const go = (k: number) => { const n = rows[Math.max(0, Math.min(rows.length - 1, k))]; if (n) { setSel(n.id); virt.scrollToIndex(rows.indexOf(n)) } }
    const handled = () => e.preventDefault()
    switch (e.key) {
      case 'ArrowDown': handled(); return go(i + 1)
      case 'ArrowUp': handled(); return go(i < 0 ? 0 : i - 1)
      case 'Home': handled(); return go(0)
      case 'End': handled(); return go(rows.length - 1)
      case 'ArrowRight': handled(); if (r && r.type?.items && !r.open) toggle(r.id); else go(i + 1); return
      case 'ArrowLeft': {
        handled()
        if (!r) return
        if (r.open) { toggle(r.id); return }
        for (let k = i - 1; k >= 0; k--) if (rows[k].depth < r.depth) return go(k)
        return
      }
      case 'Enter': if (r) { handled(); if (r.type?.items) toggle(r.id); else if (!locked(r) && r.type?.read && !isId(r)) setEdit({ id: r.id, what: 'value' }) } return
      case 'F2': if (r && r.path.length && !r.inList && !locked(r) && !isId(r)) { handled(); setEdit({ id: r.id, what: 'key' }) } return
      case 'Delete': if (r && !p.readOnly && (!r.path.length || (!locked(r) && !isId(r)))) { handled(); remove(r) } return
    }
  }
  const finish = () => { setEdit(null); scroll.current?.focus() }
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-8 shrink-0 items-center gap-1 border-b border-line-2 bg-panel-2 px-2">
        <Button size="sm" variant="ghost" className="!h-6" onClick={() => { setEverything(true); setToggled(new Set()) }}>Expand all</Button>
        <Button size="sm" variant="ghost" className="!h-6" onClick={() => { setEverything(false); setToggled(new Set()) }}>Collapse fields</Button>
        <Button size="sm" variant="ghost" className="!h-6" onClick={() => { setEverything(false); setToggled(new Set(p.docs.map((d) => rowId(d.key, [])))) }}>Collapse all</Button>
        {!p.readOnly && <span className="ml-auto truncate text-[11.5px] text-muted">Double-click a value to change it · right-click for more</span>}
      </div>
      <div ref={scroll} role="tree" aria-label="Documents" tabIndex={0} onKeyDown={onKey} data-testid="tree-view" className="min-h-0 flex-1 overflow-auto py-1 font-mono text-[12.5px] outline-none">
        <div className="relative" style={{ height: virt.getTotalSize() }}>
          {virt.getVirtualItems().map((it) => {
            const r = rows[it.index]
            const d = p.docs[r.doc]
            const head = !r.depth
            const can = !locked(r)
            const editing = edit?.id === r.id ? edit.what : null
            return (
              <div key={it.key} role="treeitem" aria-level={r.depth + 1} aria-expanded={r.type?.items ? r.open : undefined} aria-selected={sel === r.id} data-testid={head ? 'tree-doc' : 'tree-row'} data-path={r.path.join('.')} data-status={d.status}
                className={clsx('group absolute inset-x-0 top-0 flex items-center gap-1.5 pr-2', sel === r.id ? 'bg-selected' : 'hover:bg-hover',
                  d.status === 'deleted' && 'line-through opacity-60', head && 'border-t border-line-2 font-sans', head && d.status === 'edited' && 'bg-edited/60', head && d.status === 'inserted' && 'bg-inserted/60', head && d.status === 'deleted' && 'bg-deleted/60')}
                style={{ height: ROW, transform: `translateY(${it.start}px)`, paddingLeft: 6 + r.depth * 18 }}
                onMouseDown={() => setSel(r.id)} onContextMenu={(e) => rowMenu(e, r)}>
                <button tabIndex={-1} aria-label={r.open ? 'Collapse' : 'Expand'} className={clsx('flex size-5 shrink-0 items-center justify-center rounded text-muted hover:bg-active', !r.type?.items && 'invisible')} onClick={() => toggle(r.id)}>
                  <ChevronRight className={clsx('size-3.5 transition-transform', r.open && 'rotate-90')} />
                </button>
                {head ? (
                  <>
                    <span className="text-[11.5px] tabular-nums text-faint">{d.status === 'inserted' ? '+' : p.offset + r.doc + 1}</span>
                    {p.onOpen && d.status !== 'inserted'
                      ? <button className="truncate font-mono text-[12px] font-semibold text-accent hover:underline" onClick={() => p.onOpen!(d)}>{r.key}</button>
                      : <span className="truncate font-mono text-[12px] font-semibold text-fg-2" onDoubleClick={() => toggle(r.id)}>{r.key}</span>}
                    <span className="shrink-0 text-[11.5px] text-muted">{d.meta ?? (r.type?.items ? `${r.count} field${r.count === 1 ? '' : 's'}` : '')}</span>
                    {d.readOnly && <span className="flex items-center gap-1 truncate text-[11.5px] text-muted" title={d.readOnly}><Lock className="size-3" />{d.readOnly}</span>}
                    <Status d={d} />
                    <span className="flex-1" />
                    <span className="flex opacity-0 group-hover:opacity-100">
                      {can && r.type?.items && <IconButton className="!size-6" label={r.type.array ? 'Add an item' : 'Add a field'} onClick={() => add(r)}><Plus className="size-3.5" /></IconButton>}
                      {!p.readOnly && p.onDuplicate && d.status !== 'deleted' && !d.readOnly && <IconButton className="!size-6" label="Duplicate" onClick={() => p.onDuplicate!(r.doc)}><Files className="size-3.5" /></IconButton>}
                      {d.status === 'edited' && <IconButton className="!size-6" label="Undo the changes" onClick={() => p.onUndo(r.doc)}><Undo2 className="size-3.5" /></IconButton>}
                      {!p.readOnly && <IconButton className="!size-6" label={d.status === 'deleted' ? 'Keep the document' : 'Delete document'} onClick={() => p.onDelete(r.doc)}>{d.status === 'deleted' ? <Undo2 className="size-3.5" /> : <Trash2 className="size-3.5" />}</IconButton>}
                    </span>
                  </>
                ) : (
                  <>
                    {editing === 'key'
                      ? <Inline initial={r.key} label="Field name" onDone={(v) => { if (v === null) finish(); else { rename(r, v.trim()); scroll.current?.focus() } }} />
                      : <span className={clsx('shrink-0', r.inList ? 'text-faint' : 'text-accent')} onDoubleClick={() => { if (can && !r.inList && !isId(r)) setEdit({ id: r.id, what: 'key' }); else if (r.type?.items) toggle(r.id) }}>{r.key}</span>}
                    <span className="shrink-0 text-faint">:</span>
                    {editing === 'value' && r.type?.read
                      ? <ValueInput type={r.type} onDone={(n) => { finish(); if (n) setValue(r, n) }} />
                      : r.type?.items
                        ? <span className="shrink-0 cursor-pointer text-muted" onClick={() => toggle(r.id)}>{r.type.array ? `[ ${r.count} item${r.count === 1 ? '' : 's'} ]` : `{ ${r.count} field${r.count === 1 ? '' : 's'} }`}</span>
                        : <span className={clsx('min-w-0 truncate', VALUE[r.type?.label ?? ''] ?? 'text-fg-2')} title={r.type?.text?.slice(0, 2000)}
                            onDoubleClick={() => { if (can && r.type?.read && !isId(r)) setEdit({ id: r.id, what: 'value' }) }}>
                            {r.type?.label === 'String' ? JSON.stringify((r.type.text ?? '').slice(0, 1000)) : r.type?.text}
                          </span>}
                    <span className="ml-1 shrink-0 font-sans text-[10.5px] text-faint">{r.type?.label}</span>
                    <span className="flex-1" />
                    {can && !isId(r) && (
                      <span className="flex shrink-0 font-sans opacity-0 group-hover:opacity-100">
                        {r.type?.items && <IconButton className="!size-6" label={r.type.array ? 'Add an item' : 'Add a field'} onClick={() => add(r)}><Plus className="size-3.5" /></IconButton>}
                        {r.type?.read && <IconButton className="!size-6" label={`Edit ${r.key}`} onClick={() => setEdit({ id: r.id, what: 'value' })}><Pencil className="size-3.5" /></IconButton>}
                        {r.path.length > 0 && <IconButton className="!size-6" label={`Remove ${r.key}`} onClick={() => remove(r)}><Trash2 className="size-3.5" /></IconButton>}
                      </span>
                    )}
                  </>
                )}
              </div>
            )
          })}
        </div>
      </div>
      {menu.node}
    </div>
  )
}

const VALUE: Record<string, string> = { String: 'text-ok', Number: 'text-warn', Boolean: 'text-warn', Null: 'text-faint italic', Long: 'text-warn', Int: 'text-warn', Double: 'text-warn', Decimal: 'text-warn' }

/** Inline edits a name in its place; null when given up. */
function Inline({ initial, label, onDone }: { initial: string; label: string; onDone: (v: string | null) => void }) {
  const [v, setV] = useState(initial)
  const done = useRef(false)
  const end = (x: string | null) => { if (!done.current) { done.current = true; onDone(x) } }
  return (
    <input autoFocus aria-label={label} value={v} onChange={(e) => setV(e.target.value)} onFocus={(e) => e.target.select()} onBlur={() => end(v)} onMouseDown={(e) => e.stopPropagation()}
      onKeyDown={(e) => { e.stopPropagation(); if (e.key === 'Enter') end(v); if (e.key === 'Escape') end(null) }}
      className="h-[22px] w-44 rounded border border-accent bg-panel px-1 font-mono text-[12.5px] outline-none" />
  )
}

function ValueInput({ type, onDone }: { type: JType; onDone: (n: JNode | null) => void }) {
  const [v, setV] = useState(type.text ?? '')
  const [bad, setBad] = useState('')
  const done = useRef(false)
  const end = (n: JNode | null) => { if (!done.current) { done.current = true; onDone(n) } }
  const save = (text = v, leaving = false) => {
    if (text === type.text) return end(null)
    try { end(type.read!(text)) } catch (e) { if (leaving) end(null); else setBad((e as Error).message) }
  }
  if (type.label === 'Boolean') {
    return (
      <select autoFocus aria-label="Value" value={v} onChange={(e) => save(e.target.value)} onBlur={() => end(null)} onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => { e.stopPropagation(); if (e.key === 'Escape' || e.key === 'Enter') end(null) }} className="h-[22px] rounded border border-accent bg-panel px-1 outline-none">
        <option value="true">true</option><option value="false">false</option>
      </select>
    )
  }
  const multi = type.label === 'String' && /\n/.test(v)
  const cls = 'min-w-0 flex-1 rounded border border-accent bg-panel px-1 font-mono text-[12.5px] outline-none'
  return (
    <span className="relative flex min-w-0 max-w-[620px] flex-1" onMouseDown={(e) => e.stopPropagation()}>
      {multi
        ? <textarea autoFocus aria-label="Value" rows={Math.min(10, v.split('\n').length + 1)} value={v} onChange={(e) => { setV(e.target.value); setBad('') }} onBlur={() => save(v, true)}
            onKeyDown={(e) => { e.stopPropagation(); if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) save(); if (e.key === 'Escape') end(null) }} className={clsx(cls, 'absolute inset-x-0 -top-3 z-20 resize-none py-1 shadow-pop')} />
        : <input autoFocus aria-label="Value" value={v} onChange={(e) => { setV(e.target.value); setBad('') }} onFocus={(e) => e.target.select()} onBlur={() => save(v, true)}
            onKeyDown={(e) => { e.stopPropagation(); if (e.key === 'Enter') save(); if (e.key === 'Escape') end(null) }} className={clsx(cls, 'h-[22px]', bad && '!border-err')} />}
      {bad && <span role="alert" className="absolute left-0 top-full z-30 mt-0.5 whitespace-nowrap rounded-md border border-err/40 bg-panel px-2 py-1 font-sans text-[11.5px] text-err shadow-pop">{bad}</span>}
    </span>
  )
}
