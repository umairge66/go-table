import clsx from 'clsx'
import { ChevronDown, ChevronRight, ClipboardPaste, Copy, Database, FolderPlus, Lock, Moon, Pencil, Plus, Search, Settings, Sun, Trash2, Zap } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { WindowControls, dragWindow, useOwnTitleBar } from '../components/WindowChrome'
import { api } from '../api'
import { Button, DriverBadge, IconButton, Input, Kbd, Modal, Spinner, TagPill, errorParts, useMenu, useToast } from '../components/ui'
import { hit, useShortcutLabel } from '../lib/shortcuts'
import { keys } from '../lib/platform'
import { subtitle } from '../lib/connections'
import { DRIVERS, ENGINES, driverInfo, type Connection, type Driver, type Group } from '../types'
import { ConnectionForm, UrlDialog, newConnection } from './ConnectionForm'
import { useUnsupported } from './DriverPicker'

export function Home({ connections, groups, reload, onConnect, connecting, theme, toggleTheme, onOpenSettings }: {
  connections: Connection[]
  groups: Group[]
  reload: () => Promise<void>
  onConnect: (c: Connection) => void
  connecting: string | null
  theme: 'light' | 'dark'
  toggleTheme: () => void
  onOpenSettings: () => void
}) {
  const toast = useToast()
  const menu = useMenu()
  const newKeys = useShortcutLabel('home.new')
  const newLabel = keys(newKeys ? `New connection (${newKeys.replace(/ /g, '+')})` : 'New connection')
  const ownTitleBar = useOwnTitleBar()
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<string | null>(connections[0]?.id ?? null)
  const [draft, setDraft] = useState<Connection | null>(null)
  const [draftPassword, setDraftPassword] = useState('')
  const [urlOpen, setUrlOpen] = useState(false)
  const [groupDialog, setGroupDialog] = useState<{ id?: string; name: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<Connection | null>(null)

  const matches = (c: Connection) => {
    const q = search.trim().toLowerCase()
    return !q || [c.name, c.host, c.database, c.path, c.driver, c.tag, c.user].some((x) => x?.toLowerCase().includes(q))
  }
  const sections = useMemo(() => {
    const byGroup = (gid: string) => connections.filter((c) => (c.group_id ?? '') === gid && matches(c)).sort((a, b) => a.name.localeCompare(b.name))
    return [{ group: null as Group | null, items: byGroup('') }, ...groups.map((g) => ({ group: g, items: byGroup(g.id) }))]
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connections, groups, search])
  const flat = sections.flatMap((s) => (s.group && !s.group.expanded && !search ? [] : s.items))

  const current = draft ?? connections.find((c) => c.id === selected) ?? null
  const startNew = (driver?: Driver) => { setDraft(newConnection(driver)); setDraftPassword(''); setSelected(null) }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (hit(e, 'home.new')) { e.preventDefault(); startNew() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const remove = async (c: Connection) => {
    try {
      await api.deleteConnection(c.id)
      await reload()
      if (selected === c.id) setSelected(null)
      toast('ok', 'Connection deleted', c.name)
    } catch (e) { toast('error', ...errorParts(e)) }
  }
  const duplicate = async (c: Connection) => {
    try {
      const d = await api.duplicateConnection(c.id)
      await reload()
      setSelected(d.id)
      setDraft(null)
    } catch (e) { toast('error', ...errorParts(e)) }
  }
  const toggleGroup = async (g: Group) => { await api.updateGroup(g.id, { expanded: !g.expanded }); reload() }
  const saveGroup = async () => {
    if (!groupDialog?.name.trim()) return
    try {
      if (groupDialog.id) await api.updateGroup(groupDialog.id, { name: groupDialog.name })
      else await api.createGroup(groupDialog.name)
      setGroupDialog(null)
      reload()
    } catch (e) { toast('error', ...errorParts(e)) }
  }

  const itemMenu = (e: React.MouseEvent, c: Connection) => menu.open(e, [
    { label: 'Connect', icon: <Zap />, kbd: '↵', onSelect: () => onConnect(c) },
    { label: 'Edit', icon: <Pencil />, onSelect: () => { setDraft(null); setSelected(c.id) } },
    { label: 'Duplicate', icon: <Copy />, onSelect: () => duplicate(c) },
    'sep',
    ...groups.filter((g) => g.id !== c.group_id).map((g) => ({ label: `Move to ${g.name}`, onSelect: async () => { await api.saveConnection({ ...c, group_id: g.id }, {}); reload() } })),
    ...(c.group_id ? [{ label: 'Remove from group', onSelect: async () => { await api.saveConnection({ ...c, group_id: '' }, {}); reload() } }] : []),
    'sep',
    { label: 'Delete…', icon: <Trash2 />, danger: true, onSelect: () => setConfirmDelete(c) },
  ])

  const onListKey = (e: React.KeyboardEvent) => {
    const i = flat.findIndex((c) => c.id === selected)
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const next = flat[Math.max(0, Math.min(flat.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))]
      if (next) { setSelected(next.id); setDraft(null) }
    } else if (e.key === 'Enter' && flat[i]) onConnect(flat[i])
  }

  return (
    <div className="flex h-full">
      <aside className="flex w-[280px] shrink-0 flex-col border-r border-line bg-sidebar max-[1080px]:w-[232px]">
        <div className="flex select-none items-center gap-2 px-3 pb-3 pt-4" onMouseDown={ownTitleBar ? dragWindow : undefined}>
          <div className="flex size-7 items-center justify-center rounded-lg bg-accent text-accent-fg"><Database className="size-4" /></div>
          <div className="text-[14px] font-semibold tracking-tight">GoTable</div>
          <div className="flex-1" />
          <IconButton label={theme === 'dark' ? 'Light mode' : 'Dark mode'} onClick={toggleTheme}>{theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}</IconButton>
          <IconButton label="Settings" onClick={onOpenSettings}><Settings className="size-4" /></IconButton>
        </div>
        <div className="flex items-center gap-2 px-3 pb-2">
          <Input className="min-w-0 flex-1" icon={<Search />} value={search} placeholder="Search connections" onChange={(e) => setSearch(e.target.value)} aria-label="Search connections"
            onKeyDown={(e) => { if (e.key === 'Enter' && flat[0]) onConnect(flat[0]) }} />
          <Button variant="primary" className="w-8 shrink-0 !px-0" icon={<Plus className="size-4" />} onClick={() => startNew()}
            aria-label={newLabel} title={newLabel} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3 outline-none" tabIndex={0} onKeyDown={onListKey} role="listbox" aria-label="Connections">
          {sections.map(({ group, items }) => (
            <div key={group?.id ?? 'root'}>
              {group && (
                <div className="group/g mt-2 flex h-7 items-center gap-1 rounded-md px-1.5 text-[11.5px] font-medium text-muted hover:bg-hover"
                  onContextMenu={(e) => menu.open(e, [
                    { label: 'Rename group', icon: <Pencil />, onSelect: () => setGroupDialog({ id: group.id, name: group.name }) },
                    { label: 'Delete group', icon: <Trash2 />, danger: true, onSelect: async () => { await api.deleteGroup(group.id); reload() } },
                  ])}>
                  <button className="flex min-w-0 flex-1 items-center gap-1" onClick={() => toggleGroup(group)}>
                    {group.expanded || search ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                    <span className="truncate">{group.name}</span>
                    <span className="font-normal text-faint">{items.length}</span>
                  </button>
                </div>
              )}
              {(!group || group.expanded || search) && items.map((c) => {
                const active = !draft && selected === c.id
                return (
                  <div
                    key={c.id}
                    role="option"
                    aria-selected={active}
                    aria-busy={connecting === c.id || undefined}
                    onClick={() => { setSelected(c.id); setDraft(null) }}
                    onDoubleClick={() => onConnect(c)}
                    onContextMenu={(e) => itemMenu(e, c)}
                    className={clsx('group relative mt-0.5 flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-2 transition-colors',
                      active ? 'bg-panel shadow-[0_1px_2px_rgb(0_0_0/0.06)] ring-1 ring-line' : 'hover:bg-hover')}
                  >
                    <DriverBadge driver={c.driver} size={32} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{c.name}</span>
                        <TagPill tag={c.tag} className="!h-4 !px-1.5 !text-[10px] !normal-case !tracking-normal" />
                      </div>
                      <div className="truncate text-[11.5px] text-muted">{connecting === c.id ? (c.use_ssh_tunnel ? 'Connecting through SSH…' : 'Connecting…') : subtitle(c)}</div>
                    </div>
                    {connecting === c.id && <Spinner className="size-4 shrink-0 text-accent" />}
                  </div>
                )
              })}
            </div>
          ))}
          {connections.length === 0 && <div className="px-3 py-6 text-center text-[12px] text-muted">Connections you save appear here.</div>}
          {connections.length > 0 && flat.length === 0 && search && (
            <div className="px-3 py-6 text-center text-[12px] text-muted">No connection matches “{search}”.</div>
          )}
        </div>
        <div className="flex h-[57px] shrink-0 items-center gap-2 border-t border-line px-2 text-[11px] text-muted">
          <Button variant="ghost" size="sm" icon={<FolderPlus className="size-3.5" />} onClick={() => setGroupDialog({ name: '' })}>New group</Button>
          {connections.length > 0 && <span className="ml-auto truncate pr-1" title="Double-click a connection, or press Enter, to connect"><Kbd>↵</Kbd> connect</span>}
        </div>
      </aside>

      <main className="min-w-0 flex-1">
        {current ? (
          <ConnectionForm
            key={current.id}
            initial={current}
            groups={groups}
            isNew={!!draft}
            initialPassword={draft ? draftPassword : undefined}
            onCancel={draft ? () => setDraft(null) : undefined}
            onDuplicate={draft ? undefined : () => duplicate(current)}
            onDelete={draft ? undefined : () => setConfirmDelete(current)}
            onSaved={async (c) => { await reload(); setDraft(null); setSelected(c.id) }}
            onConnect={onConnect}
            connecting={connecting === current.id}
          />
        ) : (
          <div className="flex h-full flex-col">
            {ownTitleBar && (
              <div className="flex h-12 shrink-0 select-none items-center justify-end px-3" onMouseDown={dragWindow}><WindowControls /></div>
            )}
            <Start title={connections.length ? 'Pick a connection' : 'Welcome to GoTable'} newKeys={newKeys} onNew={startNew} onPaste={() => setUrlOpen(true)}>
              {connections.length
                ? 'Select one on the left to edit it, double-click it to connect, or start a new one.'
                : 'One app for your SQL and NoSQL databases. Choose yours to set up the first connection.'}
            </Start>
          </div>
        )}
      </main>

      {urlOpen && <UrlDialog onClose={() => setUrlOpen(false)} onParsed={(c, pw) => {
        setUrlOpen(false)
        const d = { ...newConnection(c.driver), ...c, id: newConnection().id }
        setDraft(d)
        setDraftPassword(pw)
      }} />}
      {groupDialog && (
        <Modal title={groupDialog.id ? 'Rename group' : 'New group'} onClose={() => setGroupDialog(null)}
          footer={<><Button variant="ghost" onClick={() => setGroupDialog(null)}>Cancel</Button><Button variant="primary" onClick={saveGroup}>{groupDialog.id ? 'Rename' : 'Create'}</Button></>}>
          <Input autoFocus value={groupDialog.name} placeholder="e.g. Production servers" onChange={(e) => setGroupDialog({ ...groupDialog, name: e.target.value })}
            onKeyDown={(e) => e.key === 'Enter' && saveGroup()} />
        </Modal>
      )}
      {confirmDelete && (
        <Modal title={`Delete “${confirmDelete.name}”?`} subtitle="The connection and its saved passwords are removed. The database itself is not touched." onClose={() => setConfirmDelete(null)}
          footer={<><Button variant="ghost" onClick={() => setConfirmDelete(null)}>Cancel</Button><Button variant="danger" onClick={() => { remove(confirmDelete); setConfirmDelete(null) }}>Delete</Button></>}>
          <div />
        </Modal>
      )}
      {menu.node}
    </div>
  )
}

/** Start offers a new connection: by database, from a URL, or the whole list. */
function Start({ title, newKeys, onNew, onPaste, children }: {
  title: string
  newKeys: string
  onNew: (d?: Driver) => void
  onPaste: () => void
  children: ReactNode
}) {
  const unsupported = useUnsupported()
  return (
    <div className="min-h-0 flex-1 overflow-y-auto animate-fade">
      <div className="mx-auto flex min-h-full max-w-[760px] flex-col justify-center px-8 py-8">
        <h1 className="text-[26px] font-semibold leading-tight tracking-tight">{title}</h1>
        <p className="mt-2 max-w-[640px] text-[14px] leading-relaxed text-muted">{children}</p>
        <div className="mt-5 flex flex-wrap gap-2">
          <Button variant="primary" icon={<Plus className="size-3.5" />} onClick={() => onNew()} kbd={newKeys}>New connection</Button>
          <Button icon={<ClipboardPaste className="size-3.5" />} onClick={onPaste}>Paste a URL</Button>
        </div>
        <div className="mt-8 grid grid-cols-[repeat(auto-fill,minmax(156px,1fr))] gap-2">
          {ENGINES.map((d) => (
            <button key={d} type="button" disabled={unsupported(d)} onClick={() => onNew(d)}
              title={unsupported(d) ? `${driverInfo(d).label} isn't available in this build of GoTable` : undefined}
              className="flex h-12 items-center gap-2.5 rounded-xl border border-line bg-panel px-2.5 text-left text-[13px] font-medium transition-colors enabled:hover:border-accent/50 enabled:hover:bg-accent-soft disabled:opacity-40">
              <DriverBadge driver={d} size={28} decorative />
              <span className="truncate">{driverInfo(d).label}</span>
            </button>
          ))}
        </div>
        <div className="mt-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-2 text-[12.5px] text-muted">
          <button type="button" className="rounded font-medium text-accent hover:underline" onClick={() => onNew()}>
            And {DRIVERS.length - ENGINES.length} compatible servers and cloud services
          </button>
          <span className="inline-flex items-center gap-1.5"><Lock className="size-3.5" aria-hidden="true" />Passwords are kept in your system keyring, never in plain files.</span>
        </div>
      </div>
    </div>
  )
}
