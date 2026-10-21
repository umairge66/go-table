import clsx from 'clsx'
import { ChevronDown, ChevronRight, Copy, Database, FolderPlus, Moon, Pencil, Plus, Search, Sun, Trash2, Zap } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { WindowControls, dragWindow, useOwnTitleBar } from '../components/WindowChrome'
import { api } from '../api'
import { Button, DriverBadge, EmptyState, IconButton, Input, Kbd, Modal, Spinner, TagPill, errorParts, useMenu, useToast } from '../components/ui'
import { tagInfo, type Connection, type Group } from '../types'
import { ConnectionForm, UrlDialog, newConnection } from './ConnectionForm'

export function subtitle(c: Connection) {
  if (c.driver === 'sqlite') return c.path ?? ''
  let s = `${c.host || '127.0.0.1'}${c.port ? ':' + c.port : ''}`
  if (c.user) s = `${c.user}@${s}`
  if (c.database) s += `/${c.database}`
  return s
}

export function Home({ connections, groups, reload, onConnect, connecting, theme, toggleTheme }: {
  connections: Connection[]
  groups: Group[]
  reload: () => Promise<void>
  onConnect: (c: Connection) => void
  connecting: string | null
  theme: 'light' | 'dark'
  toggleTheme: () => void
}) {
  const toast = useToast()
  const menu = useMenu()
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
  const startNew = () => { setDraft(newConnection()); setDraftPassword(''); setSelected(null) }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'n') { e.preventDefault(); startNew() }
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
      <aside className="flex w-[300px] shrink-0 flex-col border-r border-line bg-sidebar">
        <div className="flex select-none items-center gap-2 px-3 pt-3 pb-2" onMouseDown={ownTitleBar ? dragWindow : undefined}>
          <div className="flex size-7 items-center justify-center rounded-lg bg-accent text-accent-fg"><Database className="size-4" /></div>
          <div className="text-[14px] font-semibold">GoTable</div>
          <div className="flex-1" />
          <IconButton label={theme === 'dark' ? 'Light mode' : 'Dark mode'} onClick={toggleTheme}>{theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}</IconButton>
          <IconButton label="New group" onClick={() => setGroupDialog({ name: '' })}><FolderPlus className="size-4" /></IconButton>
          <IconButton label="New connection (Ctrl+N)" onClick={startNew}><Plus className="size-4" /></IconButton>
        </div>
        <div className="px-3 pb-2">
          <Input icon={<Search />} value={search} placeholder="Search connections" onChange={(e) => setSearch(e.target.value)} aria-label="Search connections"
            onKeyDown={(e) => { if (e.key === 'Enter' && flat[0]) onConnect(flat[0]) }} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3 outline-none" tabIndex={0} onKeyDown={onListKey} role="listbox" aria-label="Connections">
          {sections.map(({ group, items }) => (
            <div key={group?.id ?? 'root'}>
              {group && (
                <div className="group/g mt-2 flex h-7 items-center gap-1 rounded-md px-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted hover:bg-hover"
                  onContextMenu={(e) => menu.open(e, [
                    { label: 'Rename group', icon: <Pencil />, onSelect: () => setGroupDialog({ id: group.id, name: group.name }) },
                    { label: 'Delete group', icon: <Trash2 />, danger: true, onSelect: async () => { await api.deleteGroup(group.id); reload() } },
                  ])}>
                  <button className="flex flex-1 items-center gap-1" onClick={() => toggleGroup(group)}>
                    {group.expanded || search ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                    {group.name}
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
                    {c.tag && <span className="absolute left-0 top-2 bottom-2 w-[3px] rounded-r" style={{ background: tagInfo(c.tag).color }} />}
                    <DriverBadge driver={c.driver} size={30} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-[13px] font-medium">{c.name}</span>
                        <TagPill tag={c.tag} className="!h-4 !px-1.5 !text-[9.5px]" />
                      </div>
                      <div className="truncate text-[11.5px] text-muted">{connecting === c.id ? (c.use_ssh_tunnel ? 'Connecting through SSH…' : 'Connecting…') : subtitle(c)}</div>
                    </div>
                    {connecting === c.id && <Spinner className="size-4 shrink-0 text-accent" />}
                  </div>
                )
              })}
            </div>
          ))}
          {connections.length > 0 && flat.length === 0 && search && (
            <div className="px-3 py-6 text-center text-[12px] text-muted">No connection matches “{search}”.</div>
          )}
        </div>
        <div className="border-t border-line px-3 py-2 text-[11px] text-faint">
          <Kbd>↑</Kbd> <Kbd>↓</Kbd> select · <Kbd>↵</Kbd> connect · double-click opens
        </div>
      </aside>

      <main className="min-w-0 flex-1 overflow-y-auto">
        {ownTitleBar && (
          <div className="sticky top-0 z-20 flex h-10 select-none items-center justify-end bg-bg/80 px-2 backdrop-blur-sm" onMouseDown={dragWindow}>
            <WindowControls />
          </div>
        )}
        {current ? (
          <div className="px-8 pt-10">
            <ConnectionForm
              key={current.id}
              initial={current}
              groups={groups}
              isNew={!!draft}
              initialPassword={draft ? draftPassword : undefined}
              onCancel={draft ? () => setDraft(null) : undefined}
              onSaved={async (c) => { await reload(); setDraft(null); setSelected(c.id) }}
              onConnect={onConnect}
              connecting={connecting === current.id}
            />
          </div>
        ) : (
          <EmptyState icon={<Database />} title={connections.length ? 'Pick a connection' : 'Welcome to GoTable'}
            actions={<>
              <Button variant="primary" icon={<Plus className="size-3.5" />} onClick={startNew} kbd="Ctrl N">New connection</Button>
              <Button onClick={() => setUrlOpen(true)}>Paste a URL</Button>
            </>}>
            {connections.length
              ? 'Select a connection on the left to edit it, or double-click it to connect.'
              : 'Connect to PostgreSQL, MySQL, MariaDB or SQLite. Passwords are kept in your system keyring, never in plain files.'}
          </EmptyState>
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
