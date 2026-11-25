import clsx from 'clsx'
import { AlertTriangle, Eye, EyeOff, KeyRound, Lock, LockOpen, Pencil, Plus, Search, ShieldCheck, Trash2, UserRound, UsersRound, Wand2, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, type ApiError, type UserOp } from '../api'
import { SqlEditor } from '../components/SqlEditor'
import { Button, DragHandle, Field, IconButton, Input, Kbd, Modal, RefreshButton, Select, Spinner, Switch, errorParts, useMenu, useResizable, useToast, type MenuEntry } from '../components/ui'
import { HISTORY_EVENT, notify } from './SidebarPanels'
import { family, type Account, type DbObject, type Grant, type GrantLevel, type SessionInfo, type UserAdmin } from '../types'

const idOf = (u: Pick<Account, 'name' | 'host'>) => `${u.name}\u0001${u.host}`
const called = (u: Pick<Account, 'name' | 'host'>) => (u.host ? `${u.name}@${u.host}` : u.name)
/** plainName: a MySQL pattern such as my\_db as the database it usually stands for. */
const plainName = (pattern: string) => pattern.replace(/\\([_%\\])/g, '$1')

type Dialog =
  | { kind: 'create' }
  | { kind: 'password'; user: Account }
  | { kind: 'drop'; users: Account[] }
  | { kind: 'grant'; users: Account[]; grant?: Grant }
  | { kind: 'ops'; title: string; done: string; ops: UserOp[]; danger?: boolean }

/** UsersView manages a server's accounts: who they are, and what they may do. */
export function UsersView({ session, schema, schemas, objects, active }: {
  session: SessionInfo
  schema: string
  schemas: string[]
  objects: DbObject[] | null
  active: boolean
}) {
  const [data, setData] = useState<{ users: Account[]; admin: UserAdmin } | null>(null)
  const [error, setError] = useState<[string, string] | null>(null)
  const [busy, setBusy] = useState(false)
  const [search, setSearch] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  const [grants, setGrants] = useState<Grant[] | null>(null)
  const [grantsError, setGrantsError] = useState('')
  const [dialog, setDialog] = useState<Dialog | null>(null)
  // checked: several users, for what is done to all of them.
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const anchor = useRef<string | null>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const menu = useMenu()
  const [listW, dragList] = useResizable(272, 200, 520, 'x', false, 'gotable:usersList')
  const load = useCallback(async () => {
    setBusy(true)
    try {
      const r = await api.users(session.id)
      setData(r)
      setError(null)
      setSel((s) => (s && r.users.some((u) => idOf(u) === s) ? s : r.users.length ? idOf(r.users.find((u) => !u.system) ?? r.users[0]) : null))
    } catch (e) { setError(errorParts(e)) } finally { setBusy(false) }
  }, [session.id])
  useEffect(() => { load() }, [load])
  // Those that are gone aren't selected any more.
  useEffect(() => { if (data) setChecked((s) => { const n = new Set([...s].filter((x) => data.users.some((u) => idOf(u) === x))); return n.size === s.size ? s : n }) }, [data])
  useEffect(() => {
    const on = () => { if (active) load() }
    window.addEventListener('gotable:refresh-shortcut', on)
    return () => window.removeEventListener('gotable:refresh-shortcut', on)
  }, [active, load])
  const user = data?.users.find((u) => idOf(u) === sel)
  const loadGrants = useCallback(async () => {
    if (!user) return
    try {
      setGrants((await api.userGrants(session.id, user.name, user.host)).grants)
      setGrantsError('')
    } catch (e) { setGrants([]); setGrantsError(errorParts(e).filter(Boolean).join(' — ')) }
  }, [session.id, user?.name, user?.host]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { setGrants(null); loadGrants() }, [loadGrants])

  const done = (name?: { name: string; host: string }) => {
    setDialog(null)
    if (name) { setSel(idOf(name)); setChecked(new Set()) }
    notify(HISTORY_EVENT)
    load()
    loadGrants()
  }
  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
        <div className="text-[14px] font-semibold text-err">{error[0]}</div>
        <div className="max-w-xl break-words text-[12.5px] text-muted">{error[1]}</div>
        <Button size="sm" className="mt-2" onClick={load}>Try again</Button>
      </div>
    )
  }
  if (!data) return <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div>
  const { admin } = data
  const q = search.trim().toLowerCase()
  const matching = data.users.filter((u) => !q || called(u).toLowerCase().includes(q))
  const several = data.users.filter((u) => checked.has(idOf(u)))
  const sections: [string, Account[]][] = [['Users', matching.filter((u) => !u.role && !u.system)], ['Roles', matching.filter((u) => u.role && !u.system)], ['The server’s own', matching.filter((u) => u.system)]]
  const isMe = (u: Account) => admin.me === u.name || admin.me === called(u)
  // The server's own accounts, and the one signed in as, are never part of it.
  const pickable = sections.flatMap(([, us]) => us).filter((u) => !u.system && !isMe(u))
  const allPicked = pickable.length > 0 && pickable.every((u) => checked.has(idOf(u)))
  const toggle = (u: Account) => setChecked((s) => { const n = new Set(s); if (n.has(idOf(u))) n.delete(idOf(u)); else n.add(idOf(u)); return n })
  const range = (u: Account) => {
    const order = pickable.map(idOf)
    const a = order.indexOf(anchor.current ?? sel ?? ''), b = order.indexOf(idOf(u))
    if (a < 0 || b < 0) { toggle(u); return }
    setChecked((s) => new Set([...s, ...order.slice(Math.min(a, b), Math.max(a, b) + 1)]))
  }
  const onPick = (e: React.MouseEvent, u: Account) => {
    const can = pickable.includes(u)
    if (can && (e.ctrlKey || e.metaKey)) {
      // The one that was open comes along, as in a file list.
      const open = pickable.find((x) => idOf(x) === sel)
      if (!checked.size && open && open !== u) setChecked(new Set([idOf(open), idOf(u)]))
      else toggle(u)
    } else if (can && e.shiftKey) range(u)
    else { setSel(idOf(u)); setChecked(new Set()) }
    if (!e.shiftKey) anchor.current = idOf(u)
  }
  const onListKey = (e: React.KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') { e.preventDefault(); setChecked(new Set(pickable.map(idOf))) }
    else if (e.key === 'Escape' && checked.size) { e.preventDefault(); setChecked(new Set()) }
    else if (e.key === 'Delete') {
      const us = several.length ? several : pickable.filter((u) => idOf(u) === sel)
      if (us.length) { e.preventDefault(); setDialog({ kind: 'drop', users: us }) }
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const order = sections.flatMap(([, us]) => us).map(idOf)
      const next = order[Math.max(0, Math.min(order.length - 1, order.indexOf(sel ?? '') + (e.key === 'ArrowDown' ? 1 : -1)))]
      if (!next) return
      setSel(next)
      if (e.shiftKey) setChecked((s) => new Set([...s, ...pickable.map(idOf).filter((x) => x === next || x === sel)]))
      else { setChecked(new Set()); anchor.current = next }
      listRef.current?.querySelector(`[data-user="${CSS.escape(next)}"]`)?.scrollIntoView({ block: 'nearest' })
    }
  }
  const opsFor = (us: Account[], x: Omit<UserOp, 'name' | 'host'>): UserOp[] => us.map((u) => ({ ...x, name: u.name, host: u.host }))
  const people = (us: Account[]) => `${us.length} user${us.length === 1 ? '' : 's'}`
  const op = (x: Omit<UserOp, 'name' | 'host'>): UserOp => ({ ...x, name: user!.name, host: user!.host })
  const ask = (title: string, finished: string, ops: UserOp[], danger = false) => setDialog({ kind: 'ops', title, done: finished, ops, danger })
  const bulkLock = (us: Account[], lock: boolean) => {
    const those = us.filter((u) => !u.role && u.locked !== lock)
    ask(`${lock ? 'Lock' : 'Unlock'} ${people(those)}`, `${lock ? 'Locked' : 'Unlocked'} ${people(those)}`, opsFor(those, { kind: lock ? 'lock' : 'unlock' }), lock)
  }
  const bulkItems = (us: Account[]): MenuEntry[] => [
    { label: us.length > 1 ? `Give ${us.length} users access…` : 'Give access…', icon: <Plus />, onSelect: () => setDialog({ kind: 'grant', users: us }) },
    ...(admin.canLock && us.some((u) => !u.locked && !u.role) ? [{ label: 'Lock', icon: <Lock />, onSelect: () => bulkLock(us, true) }] : []),
    ...(admin.canLock && us.some((u) => u.locked) ? [{ label: 'Unlock', icon: <LockOpen />, onSelect: () => bulkLock(us, false) }] : []),
    'sep',
    { label: us.length > 1 ? `Delete ${us.length} users…` : 'Delete…', icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => setDialog({ kind: 'drop', users: us }) },
  ]
  const levelOf = (g: Grant) => admin.levels.find((l) => l.key === g.level)
  const roles = [...new Set([...(user?.roles ?? []), ...(grants ?? []).filter((g) => g.level === 'role').flatMap((g) => g.privileges)])]
  const addable = admin.roles.filter((r) => !roles.includes(r) && r !== user?.name)
  const places = (grants ?? []).filter((g) => g.level !== 'role')
  const superuser = !!user && (user.attributes.rolsuper || places.some((g) => g.level === 'global' && g.privileges.some((x) => x === 'ALL PRIVILEGES' || x === 'ALL' || x === 'root')))

  return (
    <div className="flex h-full" data-testid="users" aria-busy={busy}>
      <aside className="flex shrink-0 flex-col bg-panel-2" style={{ width: listW }}>
        <div className="flex items-center gap-1.5 p-2.5">
          <Input className="min-w-0 flex-1" icon={<Search />} value={search} placeholder="Find a user" aria-label="Find a user" onChange={(e) => setSearch(e.target.value)} />
          <RefreshButton label="Refresh users" iconClassName="size-3.5" busy={busy} onRefresh={load} />
        </div>
        {pickable.length > 1 && (
          <label className="flex h-7 shrink-0 cursor-pointer items-center gap-2 px-4 text-[11.5px] text-muted">
            <input type="checkbox" className="accent-[var(--accent)]" aria-label="Select all users" checked={allPicked} ref={(el) => { if (el) el.indeterminate = !allPicked && several.length > 0 }}
              onChange={() => setChecked(allPicked ? new Set() : new Set(pickable.map(idOf)))} />
            {several.length ? <span><b className="font-semibold text-fg-2">{several.length}</b> selected</span> : q ? `Select the ${pickable.length} found` : `Select all ${pickable.length}`}
          </label>
        )}
        <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-3 outline-none" role="listbox" aria-label="Users" aria-multiselectable="true" tabIndex={0} onKeyDown={onListKey}>
          {sections.map(([title, us]) => us.length > 0 && (
            <div key={title}>
              <div className="px-2 pb-1 pt-2 text-[11px] font-medium text-muted">{title}</div>
              {us.map((u) => (
                <div key={idOf(u)} role="option" data-user={idOf(u)} aria-selected={several.length ? checked.has(idOf(u)) : sel === idOf(u)} title={called(u)} onClick={(e) => onPick(e, u)}
                  onContextMenu={(e) => { if (!pickable.includes(u)) return; const us = checked.has(idOf(u)) ? several : [u]; if (!checked.has(idOf(u))) { setSel(idOf(u)); setChecked(new Set()) } menu.open(e, bulkItems(us)) }}
                  className={clsx('group flex h-9 cursor-pointer select-none items-center gap-2 rounded-lg px-2 text-[13px]', checked.has(idOf(u)) ? 'bg-selected' : !several.length && sel === idOf(u) ? 'bg-active font-medium' : 'hover:bg-hover', u.system && 'text-muted')}>
                  <span className="relative flex size-4 shrink-0 items-center justify-center">
                    {pickable.includes(u) && <input type="checkbox" aria-label={`Select ${called(u)}`} tabIndex={-1} checked={checked.has(idOf(u))} onClick={(e) => { e.stopPropagation(); if (e.shiftKey) range(u); else anchor.current = idOf(u) }} onChange={(e) => { if (!(e.nativeEvent as MouseEvent).shiftKey) toggle(u) }}
                      className={clsx('absolute inset-0 m-auto accent-[var(--accent)]', several.length ? 'opacity-100' : 'opacity-0 group-hover:opacity-100')} />}
                    <span className={clsx(pickable.includes(u) && (several.length ? 'invisible' : 'group-hover:invisible'))}>{u.role ? <UsersRound className="size-4 text-muted" /> : <UserRound className={clsx('size-4', u.locked ? 'text-faint' : 'text-accent')} />}</span>
                  </span>
                  <span className="flex min-w-0 flex-1 items-baseline gap-1"><UserName name={u.name} />{u.host && <span className="max-w-[45%] shrink-0 truncate text-[11.5px] font-normal text-muted">@{u.host}</span>}</span>
                  <span className="ml-auto flex shrink-0 items-center gap-1">
                    {u.locked && <Lock className="size-3 text-warn" aria-label="locked" />}
                    {u.attributes.rolsuper && <ShieldCheck className="size-3.5 text-warn" aria-label="superuser" />}
                    {isMe(u) && <span className="rounded bg-accent-soft px-1 text-[10px] font-semibold text-accent">you</span>}
                  </span>
                </div>
              ))}
            </div>
          ))}
          {!matching.length && <div className="px-3 py-6 text-center text-[12px] text-muted">No user matches “{search}”.</div>}
        </div>
        <div data-bottom-bar className="border-t border-line p-2"><Button className="w-full" variant="primary" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'create' })}>New user</Button></div>
      </aside>
      <DragHandle onMouseDown={dragList} />

      {several.length > 0 ? (
        <div className="min-w-0 flex-1 overflow-y-auto" data-testid="users-bulk">
          <div className="max-w-[860px] space-y-5 p-6">
            <div className="flex items-center gap-3">
              <div className="flex size-11 items-center justify-center rounded-2xl bg-accent-soft text-accent"><UsersRound className="size-5" /></div>
              <div className="min-w-0 flex-1">
                <div className="text-[17px] font-semibold tracking-tight">{people(several)} selected</div>
                <div className="text-[12.5px] text-muted">What you do here is done to each of them. <Kbd>Esc</Kbd> ends the selection.</div>
              </div>
              <Button size="sm" variant="ghost" onClick={() => setChecked(new Set())}>Clear</Button>
            </div>
            <div className="flex flex-wrap gap-2" role="toolbar" aria-label="Selected users">
              <Button icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'grant', users: several })}>Give access…</Button>
              {admin.roles.length > 0 && (
                <Select aria-label="Give a role to all" className="w-40" value="" onChange={(e) => { if (e.target.value) ask(`Give ${people(several)} the role ${e.target.value}`, `Role given to ${people(several)}`, opsFor(several.filter((u) => u.name !== e.target.value), { kind: 'grant_role', role: e.target.value })) }}>
                  <option value="">Give a role…</option>{admin.roles.map((r) => <option key={r}>{r}</option>)}
                </Select>
              )}
              {admin.canLock && several.some((u) => !u.locked && !u.role) && <Button icon={<Lock className="size-3.5" />} onClick={() => bulkLock(several, true)}>Lock</Button>}
              {admin.canLock && several.some((u) => u.locked) && <Button icon={<LockOpen className="size-3.5" />} onClick={() => bulkLock(several, false)}>Unlock</Button>}
              <div className="flex-1" />
              <Button variant="danger" icon={<Trash2 className="size-3.5" />} onClick={() => setDialog({ kind: 'drop', users: several })}>Delete {several.length}…</Button>
            </div>
            <div className="overflow-hidden rounded-xl border border-line">
              {several.map((u) => (
                <div key={idOf(u)} className="group flex h-9 items-center gap-2 border-b border-line-2 px-3 text-[13px] last:border-0">
                  {u.role ? <UsersRound className="size-4 shrink-0 text-muted" /> : <UserRound className={clsx('size-4 shrink-0', u.locked ? 'text-faint' : 'text-accent')} />}
                  <span className="min-w-0 select-text truncate">{u.name}{u.host && <span className="text-muted">@{u.host}</span>}</span>
                  {u.locked && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-medium text-warn">locked</span>}
                  <span className="min-w-0 flex-1 truncate text-right text-[11.5px] text-muted">{u.roles.join(', ')}</span>
                  <IconButton label={`Leave out ${called(u)}`} className="!size-6 opacity-0 group-hover:opacity-100" onClick={() => toggle(u)}><X className="size-3.5" /></IconButton>
                </div>
              ))}
            </div>
          </div>
        </div>
      ) : user ? (
        <div className="min-w-0 flex-1 overflow-y-auto">
          <div className="max-w-[860px] space-y-6 p-6">
            <div className="flex items-center gap-3">
              <div className={clsx('flex size-11 items-center justify-center rounded-2xl', user.locked ? 'bg-hover text-muted' : 'bg-accent-soft text-accent')}>{user.role ? <UsersRound className="size-5" /> : <UserRound className="size-5" />}</div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2"><span className="select-text truncate text-[17px] font-semibold tracking-tight">{user.name || '(anonymous)'}</span>
                  {user.locked && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-medium text-warn">locked</span>}
                  {superuser && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-medium text-warn">may do anything</span>}</div>
                <div className="truncate text-[12.5px] text-muted">{[user.role ? 'Role' : 'User', user.host && `${admin.hostLabel.toLowerCase()} ${user.host === '%' ? 'any (%)' : user.host}`, isMe(user) && 'signed in here', user.note].filter(Boolean).join(' · ')}</div>
              </div>
              {!user.system && <>
                {!user.role && <Button size="sm" icon={<KeyRound className="size-3.5" />} onClick={() => setDialog({ kind: 'password', user })}>Password…</Button>}
                {admin.canLock && !user.role && <Button size="sm" disabled={isMe(user)} icon={user.locked ? <LockOpen className="size-3.5" /> : <Lock className="size-3.5" />}
                  onClick={() => ask(user.locked ? `Unlock ${called(user)}` : `Lock ${called(user)}`, user.locked ? 'Unlocked' : 'Locked', [op({ kind: user.locked ? 'unlock' : 'lock' })], !user.locked)}>{user.locked ? 'Unlock' : 'Lock'}</Button>}
                <Button size="sm" variant="ghost" className="text-err" disabled={isMe(user)} icon={<Trash2 className="size-3.5" />} onClick={() => setDialog({ kind: 'drop', users: [user] })}>Delete</Button>
              </>}
            </div>
            {user.system && <div className="rounded-lg border border-line bg-panel-2 px-3 py-2 text-[12.5px] text-muted">{user.note ? `This account is ${user.note}, so it isn’t changed here.` : 'The server needs this account, so it isn’t changed here.'}</div>}

            {admin.attributes.length > 0 && (
              <Section title="May">
                <div className="grid grid-cols-2 gap-x-6 gap-y-2.5 p-4">
                  {admin.attributes.map((a) => (
                    <div key={a.key}>
                      <Switch checked={!!user.attributes[a.key]} label={<span>{a.label}</span>}
                        onChange={(v) => { if (!user.system) ask(`${v ? 'Allow' : 'Stop'}: ${a.label.toLowerCase()}`, 'Changed', [op({ kind: 'attributes', attributes: { [a.key]: v } })], !v && isMe(user)) }} />
                      {a.hint && <div className="ml-[42px] text-[11.5px] text-muted">{a.hint}</div>}
                    </div>
                  ))}
                </div>
              </Section>
            )}

            {(admin.roles.length > 0 || roles.length > 0) && (
              <Section title="Roles">
                <div className="flex flex-wrap items-center gap-1.5 p-3">
                  {roles.map((r) => (
                    <span key={r} className="inline-flex h-7 items-center gap-1 rounded-full border border-line bg-panel-2 pl-2.5 pr-1 text-[12.5px]" data-testid="user-role">
                      {r}
                      {!user.system && <button aria-label={`Take away ${r}`} className="rounded-full p-1 text-muted hover:bg-hover hover:text-err" onClick={() => ask(`Take the role ${r} from ${called(user)}`, 'Role taken away', [op({ kind: 'revoke_role', role: r })], true)}><Trash2 className="size-3" /></button>}
                    </span>
                  ))}
                  {!roles.length && <span className="text-[12.5px] text-muted">No roles.</span>}
                  {!user.system && addable.length > 0 && (
                    <Select aria-label="Give a role" className="w-40" value="" onChange={(e) => { if (e.target.value) ask(`Give ${called(user)} the role ${e.target.value}`, 'Role given', [op({ kind: 'grant_role', role: e.target.value })]) }}>
                      <option value="">Give a role…</option>
                      {addable.map((r) => <option key={r}>{r}</option>)}
                    </Select>
                  )}
                </div>
              </Section>
            )}

            <Section title={admin.privilegeWord === 'role' ? 'Roles by database' : 'Privileges'} actions={!user.system && <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'grant', users: [user] })}>Give access</Button>}>
              {grants === null ? <div className="p-4"><Spinner /></div> : grantsError ? <div className="p-4 text-[12.5px] text-err">{grantsError}</div> : !places.length ? (
                <div className="px-4 py-5 text-[12.5px] text-muted">{superuser ? 'None needed: it may do anything.' : `${user.name} has no ${admin.privilegeWord}s of its own.`}{family(session.connection.driver) === 'postgres' && !superuser && ' It may still do what its roles may, and what everyone may; and everything with what it owns.'}</div>
              ) : (
                <table className="w-full text-[12.5px]">
                  <tbody>
                    {places.map((g, i) => {
                      const level = levelOf(g)
                      const may = !user.system && !g.readOnly && (level || g.level === 'future')
                      return (
                        <tr key={i} className="group border-b border-line-2 last:border-0 hover:bg-hover" data-testid="grant">
                          <td className="w-60 px-3 py-2 align-top">
                            <div className="truncate font-medium" title={placeName(g)}>{placeName(g)}</div>
                            <div className="text-[11.5px] text-muted">{g.level === 'future' ? 'Tables made later' : g.level === 'sequence' ? 'Sequence' : g.level === 'routine' ? 'Routine' : g.level === 'global' ? 'Every database' : (level?.label ?? g.level)}</div>
                          </td>
                          <td className="px-3 py-2">
                            <div className="flex flex-wrap gap-1">
                              {g.privileges.map((x) => <span key={x} className={clsx('rounded px-1.5 py-0.5 text-[11.5px] font-medium', /^(ALL|DROP|SUPER|GRANT|CONTROL|DELETE|TRUNCATE|root|dbOwner)/.test(x) ? 'bg-warn/15 text-warn' : 'bg-hover text-fg-2')}>{x}</span>)}
                              {g.grantOption && <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[11.5px] font-medium text-accent">may pass it on</span>}
                              {g.readOnly && <span className="text-[11.5px] text-muted">{g.readOnly}</span>}
                            </div>
                          </td>
                          <td className="w-20 px-2 py-1.5 text-right align-top">
                            {may && (
                              <span className="inline-flex gap-0.5 opacity-60 group-hover:opacity-100">
                                {level && <IconButton label={`Change access to ${placeName(g)}`} onClick={() => setDialog({ kind: 'grant', users: [user], grant: g })}><Pencil className="size-3.5" /></IconButton>}
                                <IconButton label={`Take away access to ${placeName(g)}`} onClick={() => ask(`Take away access to ${placeName(g)}`, 'Access taken away', [op({ kind: 'revoke', level: g.level, database: g.database, schema: g.schema, object: g.object, pattern: true, privileges: g.privileges, grantOption: g.grantOption })], true)}><Trash2 className="size-3.5" /></IconButton>
                              </span>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              )}
            </Section>
          </div>
        </div>
      ) : <div className="flex flex-1 items-center justify-center text-[13px] text-muted">Pick a user.</div>}

      {dialog?.kind === 'create' && <CreateDialog session={session} admin={admin} onClose={() => setDialog(null)} onDone={done} />}
      {dialog?.kind === 'password' && <PasswordDialog session={session} user={dialog.user} onClose={() => setDialog(null)} onDone={() => done()} />}
      {dialog?.kind === 'drop' && data.users.some((u) => dialog.users.some((x) => idOf(x) === idOf(u))) && <DropDialog session={session} admin={admin} users={data.users.filter((u) => dialog.users.some((x) => idOf(x) === idOf(u)))} onClose={() => setDialog(null)} onDone={() => { if (dialog.users.some((u) => idOf(u) === sel)) setSel(null); setChecked(new Set()); done() }} onFailed={load} />}
      {dialog?.kind === 'grant' && <GrantDialog session={session} admin={admin} users={dialog.users} grant={dialog.grant} schema={schema} schemas={schemas} objects={objects} onClose={() => setDialog(null)} onDone={() => done()} />}
      {dialog?.kind === 'ops' && <OpsDialog session={session} title={dialog.title} done={dialog.done} ops={dialog.ops} danger={dialog.danger} submitLabel="Run" onClose={() => setDialog(null)} onDone={() => done()} />}
      {menu.node}
    </div>
  )
}

/** UserName keeps the end of a long name in view: that is often where names differ. */
function UserName({ name }: { name: string }) {
  if (!name) return <span className="truncate">(anonymous)</span>
  const cut = name.length > 18 ? name.length - 6 : name.length
  return <span className="flex min-w-0"><span className="truncate">{name.slice(0, cut)}</span><span className="shrink-0">{name.slice(cut)}</span></span>
}

function placeName(g: Grant) {
  const db = plainName(g.database)
  if (g.level === 'global') return 'The whole server'
  if (g.level === 'future') return g.schema || 'Any schema'
  return [db, g.schema, g.object].filter(Boolean).join('.') || 'This database'
}

function Section({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section aria-label={title}>
      <div className="mb-2 flex h-7 items-center"><h3 className="text-[13px] font-semibold">{title}</h3><div className="flex-1" />{actions}</div>
      <div className="overflow-hidden rounded-xl border border-line">{children}</div>
    </section>
  )
}

/** OpsDialog is a form with the statements it makes, shown before they run. */
function OpsDialog({ session, title, subtitle, done, ops, incomplete, submitLabel, danger, blocked, width = 620, onClose, onDone, onFailed, children }: {
  session: SessionInfo
  title: string
  subtitle?: ReactNode
  done: string
  /** ops: null while the form is incomplete. */
  ops: UserOp[] | null
  incomplete?: string
  submitLabel: string
  danger?: boolean
  /** blocked: something is to be confirmed first. */
  blocked?: boolean
  width?: number
  onClose: () => void
  onDone: () => void
  /** onFailed: some of it may have run. */
  onFailed?: () => void
  children?: ReactNode
}) {
  const toast = useToast()
  const [preview, setPreview] = useState<string[]>([])
  const [problem, setProblem] = useState('')
  const [busy, setBusy] = useState(false)
  // Only that there is a password matters to the preview.
  const key = JSON.stringify(ops?.map((o) => ({ ...o, password: o.password ? 'x' : '' })))
  useEffect(() => {
    if (!ops) { setPreview([]); setProblem(''); return }
    let stale = false
    const t = setTimeout(() => {
      api.changeUsers(session.id, ops.map((o) => ({ ...o, password: o.password ? 'x' : '' })), true).then((r) => { if (!stale) { setPreview(r.statements); setProblem('') } },
        (e) => { if (!stale) { setPreview([]); setProblem((e as ApiError).detail ?? errorParts(e)[1]) } })
    }, 150)
    return () => { stale = true; clearTimeout(t) }
  }, [key, session.id]) // eslint-disable-line react-hooks/exhaustive-deps
  const ready = !!ops && preview.length > 0 && !problem && !blocked
  const production = session.connection.tag === 'production'
  const submit = async () => {
    if (!ready) return
    setBusy(true)
    try {
      await api.changeUsers(session.id, ops!, false)
      toast('ok', done)
      onDone()
    } catch (e) { setProblem(errorParts(e).filter(Boolean).join(' — ')); onFailed?.() } finally { setBusy(false) }
  }
  return (
    <Modal width={width} title={title} subtitle={subtitle} onClose={onClose}
      footer={<>
        {production && <span className="mr-auto flex items-center gap-1.5 text-[12px] font-medium text-err"><AlertTriangle className="size-3.5" /> This is PRODUCTION.</span>}
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant={danger || production ? 'danger' : 'primary'} disabled={!ready} loading={busy} onClick={submit} autoFocus={!children}>{submitLabel}</Button>
      </>}>
      <form className="space-y-3.5" aria-label={title} onSubmit={(e) => { e.preventDefault(); submit() }}>
        {children}
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">{family(session.connection.driver) === 'mongodb' ? 'What will run' : 'SQL that will run'}</div>
          {problem || !preview.length
            ? <pre data-testid="user-sql" className={clsx('min-h-[44px] whitespace-pre-wrap break-words rounded-lg border px-3 py-2.5 font-mono text-[12px]', problem ? 'border-err/30 bg-err/5 text-err' : 'border-line bg-panel-2 text-muted')}>{problem || (ops ? '…' : incomplete)}</pre>
            : <div data-testid="user-sql" className="max-h-44 overflow-auto rounded-lg border border-line" style={{ height: Math.min(176, 20 + preview.reduce((n, l) => n + Math.ceil(l.length / 74), 0) * 22) }}><SqlEditor key={preview.join()} value={preview.join(';\n') + ';'} driver={session.connection.driver} readOnly wrap /></div>}
        </div>
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}

function PasswordInput({ value, onChange, label = 'Password', hint }: { value: string; onChange: (v: string) => void; label?: string; hint?: string }) {
  const [show, setShow] = useState(false)
  // Letters and digits that can't be mistaken for one another, and signs no shell or URL minds.
  const generate = () => {
    const set = 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789-_.'
    let s = ''
    while (!/[a-z]/.test(s) || !/[A-Z]/.test(s) || !/\d/.test(s) || !/[-_.]/.test(s)) {
      s = ''
      for (const x of crypto.getRandomValues(new Uint32Array(20))) s += set[x % set.length]
    }
    onChange(s)
    setShow(true)
  }
  return (
    <Field label={label} hint={hint}>
      <div className="flex gap-1.5">
        <Input className="min-w-0 flex-1" mono={show} type={show ? 'text' : 'password'} autoComplete="new-password" value={value} aria-label={label} onChange={(e) => onChange(e.target.value)} />
        <IconButton label={show ? 'Hide' : 'Show'} className="!size-8" onClick={() => setShow(!show)}>{show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}</IconButton>
        <Button type="button" icon={<Wand2 className="size-3.5" />} onClick={generate}>Generate</Button>
      </div>
    </Field>
  )
}

function CreateDialog({ session, admin, onClose, onDone }: { session: SessionInfo; admin: UserAdmin; onClose: () => void; onDone: (u: { name: string; host: string }) => void }) {
  const [name, setName] = useState('')
  const [host, setHost] = useState(admin.hostDefault)
  const [password, setPassword] = useState('')
  const [attributes, setAttributes] = useState<Record<string, boolean>>(() => Object.fromEntries(admin.attributes.map((a) => [a.key, a.key === 'rolcanlogin'])))
  const login = attributes.rolcanlogin !== false
  const u = { name: name.trim(), host: host.trim() }
  const ops: UserOp[] | null = u.name && (!admin.hostLabel || u.host) ? [{ kind: 'create', ...u, password: login ? password : '', attributes }] : null
  return (
    <OpsDialog session={session} title="New user" done={`Made ${u.name}`} submitLabel="Create" ops={ops} incomplete="Enter a name." onClose={onClose} onDone={() => onDone(u)}>
      <div className={clsx('grid gap-3', admin.hostLabel && 'grid-cols-[1fr_200px]')}>
        <Field label="Name"><Input autoFocus value={name} aria-label="User name" autoComplete="off" onChange={(e) => setName(e.target.value)} /></Field>
        {admin.hostLabel && <Field label={admin.hostLabel} hint={admin.hostLabel === 'Host' ? '% is any; or an address it signs in from.' : undefined}><Input value={host} aria-label={admin.hostLabel} onChange={(e) => setHost(e.target.value)} /></Field>}
      </div>
      {login && <PasswordInput value={password} onChange={setPassword} hint="Never shown in the SQL below or kept in the history." />}
      {admin.attributes.length > 0 && (
        <div className="grid grid-cols-2 gap-2">
          {admin.attributes.map((a) => <Switch key={a.key} checked={!!attributes[a.key]} onChange={(v) => setAttributes((x) => ({ ...x, [a.key]: v }))} label={a.label} />)}
        </div>
      )}
    </OpsDialog>
  )
}

function PasswordDialog({ session, user, onClose, onDone }: { session: SessionInfo; user: Account; onClose: () => void; onDone: () => void }) {
  const [password, setPassword] = useState('')
  return (
    <OpsDialog session={session} title={`New password for ${called(user)}`} done="Password changed" submitLabel="Change password" width={520}
      ops={password ? [{ kind: 'password', name: user.name, host: user.host, password }] : null} incomplete="Enter the new password." onClose={onClose} onDone={onDone}>
      <PasswordInput value={password} onChange={setPassword} label="New password" hint="Connections saved with the old one, here or elsewhere, stop working." />
    </OpsDialog>
  )
}

function DropDialog({ session, admin, users, onClose, onDone, onFailed }: { session: SessionInfo; admin: UserAdmin; users: Account[]; onClose: () => void; onDone: () => void; onFailed: () => void }) {
  const [reassign, setReassign] = useState(false)
  const [typed, setTyped] = useState('')
  const many = users.length > 1
  const sure = !many || typed.trim().toUpperCase() === 'DELETE'
  const ops = useMemo<UserOp[]>(() => users.map((u) => ({ kind: 'drop', name: u.name, host: u.host, reassign })), [users, reassign])
  return (
    <OpsDialog session={session} title={many ? `Delete ${users.length} users?` : `Delete ${called(users[0])}?`} subtitle={many ? 'They can’t sign in any more. Their open connections may go on until they close.' : 'It can’t sign in any more. Its open connections may go on until they close.'}
      done={many ? `Deleted ${users.length} users` : `Deleted ${users[0].name}`} submitLabel={many ? `Delete ${users.length} users` : 'Delete'} danger width={560} blocked={!sure}
      ops={ops} onClose={onClose} onDone={onDone} onFailed={onFailed}>
      {admin.canReassign && <Switch checked={reassign} onChange={setReassign} label={<span>Hand over what {many ? 'they own' : 'it owns'} in <b className="font-medium">{admin.current}</b> to you first <span className="text-muted">— a role that owns tables or has privileges can’t be deleted otherwise</span></span>} />}
      {many && <Field label="Type DELETE to confirm"><Input autoFocus value={typed} placeholder="DELETE" aria-label="Type DELETE to confirm" onChange={(e) => setTyped(e.target.value)} /></Field>}
      {!many && !admin.canReassign && <span />}
    </OpsDialog>
  )
}

function GrantDialog({ session, admin, users, grant, schema, schemas, objects, onClose, onDone }: {
  session: SessionInfo; admin: UserAdmin; users: Account[]
  /** grant: the access that is changed; a new one without it. */
  grant?: Grant
  schema: string; schemas: string[]; objects: DbObject[] | null
  onClose: () => void; onDone: () => void
}) {
  // Databases are the schemas where there is no level between server and table.
  const byDatabase = admin.levels.some((l) => l.key === 'table' && l.needs.includes('database'))
  const [levelKey, setLevelKey] = useState(grant?.level ?? admin.levels[0].key)
  const level: GrantLevel = admin.levels.find((l) => l.key === levelKey) ?? admin.levels[0]
  const [database, setDatabase] = useState(grant ? plainName(grant.database) : admin.databases.includes(schema) ? schema : admin.databases.includes(admin.current) ? admin.current : '')
  const [sch, setSch] = useState(grant?.schema || (schemas.includes(schema) ? schema : schemas[0] ?? ''))
  const [object, setObject] = useState(grant?.object ?? '')
  const [privs, setPrivs] = useState<string[]>(grant?.privileges ?? [])
  const [option, setOption] = useState(!!grant?.grantOption)
  const [future, setFuture] = useState(true)
  const [tables, setTables] = useState<DbObject[]>([])
  const within = byDatabase ? database : sch
  useEffect(() => {
    if (!level.needs.includes('object') || !within) return
    if (within === schema && objects) { setTables(objects); return }
    let stale = false
    api.objects(session.id, within).then((o) => { if (!stale) setTables(o) }, () => { if (!stale) setTables([]) })
    return () => { stale = true }
  }, [level, within, schema, objects, session.id])
  const changeLevel = (k: string) => {
    setLevelKey(k)
    const next = admin.levels.find((l) => l.key === k)!
    setPrivs((ps) => ps.filter((x) => next.privileges.includes(x)))
  }
  const choices = useMemo(() => [...new Set([...level.privileges, ...privs.filter((x) => !level.privileges.includes(x))])], [level, privs])
  const everything = privs.find((x) => x === 'ALL PRIVILEGES' || x === 'ALL')
  const toggle = (x: string) => setPrivs((ps) => (ps.includes(x) ? ps.filter((y) => y !== x) : [...ps.filter((y) => y !== 'ALL PRIVILEGES' && y !== 'ALL'), x]))
  const where = grant
    ? { level: grant.level, database: grant.database, schema: grant.schema, object: grant.object, pattern: true }
    : { level: level.key, database: level.needs.includes('database') ? database : '', schema: level.needs.includes('schema') ? sch : '', object: level.needs.includes('object') ? object : '' }
  const complete = privs.length > 0 && (grant || level.needs.every((n) => (n === 'database' ? database : n === 'schema' ? sch : object)))
  const ops = useMemo<UserOp[] | null>(() => {
    if (!complete) return null
    if (!grant) return users.map((u) => ({ kind: 'grant', name: u.name, host: u.host, ...where, privileges: privs, grantOption: option, future: level.future && future }))
    const base = { name: users[0].name, host: users[0].host, ...where }
    const gone = grant.privileges.filter((x) => !privs.includes(x))
    const added = privs.filter((x) => !grant.privileges.includes(x))
    const out: UserOp[] = []
    if (gone.length || (grant.grantOption && !option)) {
      // Without the right to pass it on: taken away and given anew where that is how it is done.
      if (grant.grantOption && !option) {
        out.push({ kind: 'revoke', ...base, privileges: grant.privileges, grantOption: true })
        if (privs.length) out.push({ kind: 'grant', ...base, privileges: privs })
        return out
      }
      out.push({ kind: 'revoke', ...base, privileges: gone })
    }
    if (option && !grant.grantOption) out.push({ kind: 'grant', ...base, privileges: privs, grantOption: true })
    else if (added.length) out.push({ kind: 'grant', ...base, privileges: added, grantOption: option })
    return out.length ? out : null
  }, [complete, privs, option, future, levelKey, database, sch, object]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <OpsDialog session={session} title={grant ? `Access to ${placeName(grant)}` : 'Give access'} subtitle={<>for <b className="font-medium text-fg-2">{users.length > 1 ? `${users.length} users` : called(users[0])}</b></>} done={grant ? 'Access changed' : 'Access given'} submitLabel={grant ? 'Change' : 'Give access'} width={680}
      ops={ops} incomplete={grant && complete ? 'No changes yet.' : !privs.length ? `Choose at least one ${admin.privilegeWord}.` : 'Choose where.'} onClose={onClose} onDone={onDone}>
      {!grant && (
        <div className="grid grid-cols-3 gap-3">
          <Field label="On"><Select value={level.key} aria-label="Level" onChange={(e) => changeLevel(e.target.value)}>{admin.levels.map((l) => <option key={l.key} value={l.key}>{l.label}</option>)}</Select></Field>
          {level.needs.includes('database') && (
            <Field label="Database">
              <Select value={database} aria-label="Database" onChange={(e) => { setDatabase(e.target.value); setObject('') }}>
                <option value="">Choose…</option>{admin.databases.map((d) => <option key={d}>{d}</option>)}
              </Select>
            </Field>
          )}
          {level.needs.includes('schema') && (
            <Field label="Schema">
              <Select value={sch} aria-label="Schema" onChange={(e) => { setSch(e.target.value); setObject('') }}>
                {!sch && <option value="">Choose…</option>}{schemas.map((s) => <option key={s}>{s}</option>)}
              </Select>
            </Field>
          )}
          {level.needs.includes('object') && (
            <Field label="Table">
              <Select value={object} aria-label="Table" onChange={(e) => setObject(e.target.value)}>
                <option value="">Choose…</option>{tables.map((t) => <option key={t.name}>{t.name}</option>)}
              </Select>
            </Field>
          )}
        </div>
      )}
      <div>
        <div className="mb-1.5 flex items-center gap-1.5">
          <span className="text-[11.5px] font-medium text-muted">{admin.privilegeWord === 'role' ? 'Roles' : 'Privileges'}</span>
          <div className="flex-1" />
          {level.presets.map((x) => <button key={x.label} type="button" className="rounded-full border border-line px-2.5 py-0.5 text-[11.5px] text-fg-2 hover:bg-hover" onClick={() => setPrivs(x.privileges)}>{x.label}</button>)}
        </div>
        <div role="group" aria-label="Privileges" className="flex flex-wrap gap-1.5 rounded-lg border border-line p-2.5">
          {everything && <span className="w-full pb-1 text-[12px] font-medium text-warn">Everything that can be done here. Choosing single {admin.privilegeWord}s replaces it.</span>}
          {choices.map((x) => (
            <button key={x} type="button" aria-pressed={privs.includes(x)} onClick={() => toggle(x)}
              className={clsx('h-7 rounded-full border px-2.5 text-[12px]', privs.includes(x) ? 'border-accent bg-accent-soft font-medium text-accent' : 'border-line text-fg-2 hover:bg-hover')}>{x}</button>
          ))}
        </div>
      </div>
      {!grant && level.future && <div><Switch checked={future} onChange={setFuture} label={<span>Also tables made later <span className="text-muted">— by {admin.me}</span></span>} /></div>}
      {admin.grantOption && <div><Switch checked={option} onChange={setOption} label={<span>May pass it on to others</span>} /></div>}
    </OpsDialog>
  )
}
