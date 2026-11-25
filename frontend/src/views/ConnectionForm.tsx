import clsx from 'clsx'
import { CheckCircle2, ChevronDown, ClipboardPaste, Copy, FolderOpen, KeyRound, Lock, PlugZap, Route, Shield, Trash2, XCircle } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api'
import { WindowControls, dragWindow, useOwnTitleBar } from '../components/WindowChrome'
import { Button, DriverBadge, Field, IconButton, Input, Modal, Select, Switch, TagPill, errorParts, useToast } from '../components/ui'
import { driverInfo, family, features, forDriver, hostless, isBigQuery, isD1, isSnowflake, isDynamo, isElastic, isInflux, isLibSQL, isMongo, SSL_MODES, TAGS, type Connection, type Driver, type Group, type Secrets } from '../types'
import { nativeDialogs, openFileDialog } from '../lib/native'
import { examplePath } from '../lib/platform'
import { subtitle } from '../lib/connections'
import { DriverPicker } from './DriverPicker'

export const newConnection = (driver: Driver = 'postgres'): Connection => ({
  id: crypto.randomUUID().replace(/-/g, '').toUpperCase(),
  name: '',
  driver,
  host: '127.0.0.1',
  port: driverInfo(driver).port,
  user: '',
  database: '',
  ssl_mode: forDriver(SSL_MODES, driver)[0],
  ssh_port: 22,
  rows_per_page: 300,
  tag: '',
})

type TestState = { kind: 'idle' } | { kind: 'running' } | { kind: 'ok'; text: string } | { kind: 'err'; title: string; hint?: string; detail: string }

/** Section is a part of the form: what it is about beside (or, in a narrow window, above) its fields. */
function Section({ title, hint, children }: { title: string; hint: ReactNode; children: ReactNode }) {
  return (
    <section className="grid gap-x-8 gap-y-3 py-6 @2xl:grid-cols-[160px_minmax(0,1fr)]">
      <div>
        <h2 className="text-[13px] font-semibold">{title}</h2>
        <p className="mt-0.5 text-[12px] leading-relaxed text-muted">{hint}</p>
      </div>
      <div className="min-w-0 space-y-3.5">{children}</div>
    </section>
  )
}

export function ConnectionForm({ initial, initialPassword, groups, isNew, onSaved, onConnect, onCancel, onDuplicate, onDelete, connecting }: {
  initial: Connection
  initialPassword?: string
  groups: Group[]
  isNew: boolean
  onSaved: (c: Connection) => void
  onConnect: (c: Connection) => void
  onCancel?: () => void
  /** A saved connection's own actions. */
  onDuplicate?: () => void
  onDelete?: () => void
  /** connecting: this connection is being opened (SSH can take a while). */
  connecting?: boolean
}) {
  const toast = useToast()
  const ownTitleBar = useOwnTitleBar()
  // A saved connection rarely becomes another database: its list opens on demand.
  const [picking, setPicking] = useState(isNew)
  useEffect(() => setPicking(isNew), [isNew])
  const [c, setC] = useState<Connection>(initial)
  const [secrets, setSecrets] = useState<Secrets>({})
  const [test, setTest] = useState<TestState>({ kind: 'idle' })
  const [saving, setSaving] = useState(false)
  const [urlOpen, setUrlOpen] = useState(false)
  // The desktop window can pick the SSH key in the system "Open" dialog.
  const [native, setNative] = useState(false)
  useEffect(() => { nativeDialogs().then(setNative) }, [])
  const [keyCheck, setKeyCheck] = useState<{ path: string; encrypted?: boolean; problem?: string } | null>(null)
  useEffect(() => {
    setC(initial)
    setSecrets(initialPassword ? { password: initialPassword } : {})
    setTest({ kind: 'idle' })
  }, [initial, initialPassword])

  const set = <K extends keyof Connection>(k: K, v: Connection[K]) => setC((x) => ({ ...x, [k]: v }))
  const file = features(c.driver).file
  const network = !file && !isD1(c.driver) && !isSnowflake(c.driver) && !isBigQuery(c.driver)
  const dirty = useMemo(() => JSON.stringify(c) !== JSON.stringify(initial) || Object.keys(secrets).length > 0, [c, initial, secrets])

  const setDriver = (d: Driver) => { setTest({ kind: 'idle' }); setC((x) => {
    const prevDefault = driverInfo(x.driver).port
    return {
      ...x, driver: d,
      // DynamoDB has no host unless it's a local one: AWS is found by region. D1 has none.
      ...(isD1(d) || isSnowflake(d) || isBigQuery(d) ? { use_ssh_tunnel: false } : {}),
      host: hostless(d) && x.host === '127.0.0.1' || isD1(d) ? '' : !hostless(d) && !x.host && hostless(x.driver) ? '127.0.0.1' : x.host,
      // A libSQL server takes a token only.
      ...(isLibSQL(d) ? { user: '', database: '' } : isElastic(d) ? { database: '' } : isInflux(d) ? { user: '' } : {}),
      port: !x.port || x.port === prevDefault ? driverInfo(d).port : x.port,
      ssl_mode: forDriver(SSL_MODES, d).includes(x.ssl_mode ?? '') ? x.ssl_mode : forDriver(SSL_MODES, d)[0],
    }
  }) }

  // The key file is checked as it's picked or typed, before connecting.
  const keyPath = c.use_ssh_tunnel ? (c.ssh_key_path ?? '').trim() : ''
  useEffect(() => {
    if (!keyPath) { setKeyCheck(null); return }
    const t = setTimeout(() => {
      api.checkSSHKey(keyPath).then((r) => setKeyCheck({ path: keyPath, encrypted: r.encrypted, problem: r.problem }), () => setKeyCheck(null))
    }, 300)
    return () => clearTimeout(t)
  }, [keyPath])
  const keyHint = keyCheck?.path !== keyPath ? undefined
    : keyCheck.problem ? <span className="text-err" role="alert">{keyCheck.problem}</span>
      : keyCheck.encrypted && !secrets.sshPassphrase && !c.saved?.sshPassphrase ? 'This key is encrypted: enter its passphrase.'
        : undefined
  const pickKey = () => {
    openFileDialog(['sshkey']).then((f) => { if (f) set('ssh_key_path', f.path) }, (e) => toast('error', 'Could not open the file dialog', String(e.message ?? e)))
  }

  const doTest = async () => {
    setTest({ kind: 'running' })
    try {
      const r = await api.test(c, secrets)
      setTest({ kind: 'ok', text: `${r.version} · ${r.ms} ms` })
    } catch (e) {
      const err = e as ApiError
      setTest({ kind: 'err', title: err.title ?? 'Connection failed', hint: err.hint, detail: err.detail ?? String(e) })
    }
  }

  const save = async (connect: boolean) => {
    setSaving(true)
    try {
      const saved = await api.saveConnection(c, secrets)
      setSecrets({})
      onSaved(saved)
      if (connect) onConnect(saved)
      else toast('ok', 'Connection saved', saved.name)
    } catch (e) {
      const [t, b] = errorParts(e)
      setTest({ kind: 'err', title: t, detail: b })
    } finally {
      setSaving(false)
    }
  }

  const secretInput = (key: keyof Secrets, savedFlag: boolean | undefined, placeholder: string) => (
    <Input
      type="password"
      icon={<Lock />}
      value={secrets[key] ?? ''}
      placeholder={savedFlag && secrets[key] === undefined ? '•••••••• saved in keyring' : placeholder}
      onChange={(e) => setSecrets((s) => ({ ...s, [key]: e.target.value }))}
    />
  )

  return (
    <form
      className="flex h-full min-h-0 animate-fade"
      onSubmit={(e) => { e.preventDefault(); save(true) }}
      aria-label="Connection form"
    >
      {picking && <DriverPicker value={c.driver} onChange={setDriver} className="w-[248px] max-[1080px]:w-[196px]" />}

      <div className="flex min-w-0 flex-1 flex-col bg-panel">
        <div className="flex shrink-0 items-center gap-3 border-b border-line px-6 py-3.5 max-[1080px]:px-4" onMouseDown={ownTitleBar ? dragWindow : undefined}>
          <DriverBadge driver={c.driver} size={40} />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate text-[16px] font-semibold tracking-tight">{c.name || (isNew ? 'New connection' : 'Untitled')}</span>
              {!isNew && <TagPill tag={c.tag} />}
            </div>
            {isNew
              ? <div className="truncate text-[12.5px] text-muted">Choose a database, then tell GoTable where to find it.</div>
              : (
                <div className="flex min-w-0 items-center gap-1.5 text-[12.5px] text-muted">
                  <button type="button" aria-expanded={picking} aria-label={`${driverInfo(c.driver).label}: change the type`} title="Change the type" onClick={() => setPicking((x) => !x)}
                    className="-ml-1 inline-flex shrink-0 items-center gap-0.5 rounded px-1 font-medium text-fg-2 transition-colors hover:bg-hover hover:text-fg">
                    {driverInfo(c.driver).label}<ChevronDown className={clsx('size-3.5 text-muted transition-transform', picking && 'rotate-180')} aria-hidden="true" />
                  </button>
                  <span className="truncate">{subtitle(c)}</span>
                </div>
              )}
          </div>
          <Button type="button" variant="ghost" size="sm" icon={<ClipboardPaste className="size-3.5" />} onClick={() => setUrlOpen(true)} title="Fill in the form from a connection URL">Paste URL</Button>
          {onDuplicate && <IconButton label="Duplicate" onClick={onDuplicate}><Copy className="size-4" /></IconButton>}
          {onDelete && <IconButton label="Delete connection" className="enabled:hover:!text-err" onClick={onDelete}><Trash2 className="size-4" /></IconButton>}
          {ownTitleBar && <><div className="h-5 w-px bg-line" /><WindowControls className="-mr-3" /></>}
        </div>

        <div className="@container min-h-0 flex-1 overflow-y-auto">
          <div className="max-w-[860px] divide-y divide-line-2 px-6 max-[1080px]:px-4">
            <Section title="General" hint="How it appears in your list.">
              <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3">
                <Field label="Name"><Input autoFocus={isNew} value={c.name} placeholder={file ? 'Taken from the file name if empty' : 'e.g. Shop production'} onChange={(e) => set('name', e.target.value)} /></Field>
                <Field label="Group">
                  <Select value={c.group_id ?? ''} onChange={(e) => set('group_id', e.target.value)} className="w-40">
                    <option value="">No group</option>
                    {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                  </Select>
                </Field>
              </div>
              <Field label="Environment" hint={c.tag === 'production' ? 'Production gets a red title bar and asks before committing changes.' : undefined}>
                <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label="Environment">
                  {TAGS.map((t) => (
                    <button key={t.id} type="button" role="radio" aria-checked={(c.tag ?? '') === t.id} onClick={() => set('tag', t.id)}
                      className={clsx('flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-[12px] transition-colors',
                        (c.tag ?? '') === t.id ? 'font-medium text-fg' : 'border-line text-fg-2 hover:bg-hover')}
                      style={(c.tag ?? '') === t.id ? { borderColor: t.color, background: `color-mix(in srgb, ${t.color} 13%, transparent)` } : undefined}
                    >
                      <span className="size-2 rounded-full" style={t.id ? { background: t.color } : { boxShadow: `inset 0 0 0 1.5px ${t.color}` }} />
                      {t.label}
                    </button>
                  ))}
                </div>
              </Field>
            </Section>

            <Section title={file ? 'Location' : 'Server'} hint={file ? 'The file GoTable opens.' : 'Where it is and how to sign in. Passwords stay in your system keyring.'}>
              {driverInfo(c.driver).note && (
                <div className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-[12px] text-fg-2" role="note">{driverInfo(c.driver).note}</div>
              )}
              {file ? (
                <Field label="Database file" hint="Absolute path. A new file is created if it doesn't exist.">
                  <Input mono value={c.path ?? ''} placeholder={examplePath(family(c.driver) === 'duckdb' ? 'data/warehouse.duckdb' : 'data/app.sqlite')} onChange={(e) => set('path', e.target.value)} />
                </Field>
              ) : (
                <>
                  {!isD1(c.driver) && !isSnowflake(c.driver) && !isBigQuery(c.driver) && <div className="grid grid-cols-[1fr_110px] gap-3">
                    <Field label={isDynamo(c.driver) ? 'Endpoint host (DynamoDB Local)' : 'Host'}><Input value={c.host ?? ''} placeholder={driverInfo(c.driver).host ?? '127.0.0.1'} onChange={(e) => set('host', e.target.value)} /></Field>
                    <Field label="Port"><Input inputMode="numeric" value={c.srv ? '' : c.port || ''} disabled={c.srv} onChange={(e) => set('port', Number(e.target.value.replace(/\D/g, '')) || 0)} /></Field>
                  </div>}
                  {isD1(c.driver) ? <>
                    <Field label="Account ID" hint="The code in your Cloudflare dashboard's address: dash.cloudflare.com/<account ID>/…"><Input mono value={c.user ?? ''} placeholder={driverInfo(c.driver).user} onChange={(e) => set('user', e.target.value.trim())} /></Field>
                    <Field label="API token" hint="Made under My Profile → API Tokens, with the permission Account → D1 → Edit.">{secretInput('password', c.saved?.password, 'stored in your system keyring')}</Field>
                    <Field label="Database"><Input value={c.database ?? ''} placeholder={driverInfo(c.driver).database} onChange={(e) => set('database', e.target.value)} /></Field>
                  </> : isBigQuery(c.driver) ? <>
                    <Field label="Credentials file" hint="A service account's JSON key. Empty: the sign-in of “gcloud auth application-default login”.">
                      <div className="flex gap-2">
                        <Input mono icon={<KeyRound />} className="min-w-0 flex-1" value={c.key_path ?? ''} placeholder="~/keys/project-key.json" onChange={(e) => set('key_path', e.target.value)} />
                        {native && <Button type="button" className="w-8 shrink-0 !px-0" icon={<FolderOpen className="size-[18px]" />} aria-label="Choose the key file" title="Choose the key file"
                          onClick={() => openFileDialog(['sshkey']).then((f) => { if (f) set('key_path', f.path) }, (e) => toast('error', 'Could not open the file dialog', String(e.message ?? e)))} />}
                      </div>
                    </Field>
                    <div className="grid grid-cols-2 gap-3">
                      <Field label="Project"><Input value={c.database ?? ''} placeholder={driverInfo(c.driver).database} onChange={(e) => set('database', e.target.value.trim())} /></Field>
                      <Field label="Limit of a query (GB)" hint="One that would read more is refused, unbilled. 0: no limit.">
                        <Input inputMode="decimal" value={new URLSearchParams(c.options ?? '').get('limit') ?? ''} placeholder="10" onChange={(e) => { const v = e.target.value.replace(/[^\d.]/g, ''); set('options', v ? `limit=${v}` : '') }} /></Field>
                    </div>
                    <p className="text-[12px] text-muted">Queries are billed by what they read, 10 MB at least. Browsing a table without filters or sorting, its structure and exports are free.</p>
                  </> : isSnowflake(c.driver) ? <>
                    <div className="grid grid-cols-2 gap-3">
                      <Field label="Account" hint="Organization and account, joined with a dash."><Input mono value={c.host ?? ''} placeholder={driverInfo(c.driver).host} onChange={(e) => set('host', e.target.value.trim())} /></Field>
                      <Field label="User"><Input value={c.user ?? ''} placeholder={driverInfo(c.driver).user} onChange={(e) => set('user', e.target.value)} /></Field>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <Field label="Sign in with" hint={c.auth === 'browser' ? 'Your browser opens to sign in, each time GoTable connects.' : !c.auth ? 'Works only where the user needs no second factor code.' : undefined}>
                        <Select value={c.auth ?? ''} aria-label="Sign in with" onChange={(e) => set('auth', e.target.value as Connection['auth'])}>
                          <option value="">Password</option>
                          <option value="key">Key pair</option>
                          <option value="token">Access token</option>
                          <option value="browser">Browser (SSO)</option>
                        </Select>
                      </Field>
                      {c.auth !== 'browser' && <Field label={c.auth === 'key' ? 'Key passphrase' : c.auth === 'token' ? 'Access token' : 'Password'}>
                        {secretInput('password', c.saved?.password, c.auth === 'key' ? 'if the key is encrypted' : 'stored in your system keyring')}</Field>}
                    </div>
                    {c.auth === 'key' && <Field label="Private key" hint="The .p8 (PEM) file whose public key is set on the user.">
                      <div className="flex gap-2">
                        <Input mono icon={<KeyRound />} className="min-w-0 flex-1" value={c.key_path ?? ''} placeholder="~/.ssh/snowflake_key.p8" onChange={(e) => set('key_path', e.target.value)} />
                        {native && <Button type="button" className="w-8 shrink-0 !px-0" icon={<FolderOpen className="size-[18px]" />} aria-label="Choose the key file" title="Choose the key file"
                          onClick={() => openFileDialog(['sshkey']).then((f) => { if (f) set('key_path', f.path) }, (e) => toast('error', 'Could not open the file dialog', String(e.message ?? e)))} />}
                      </div>
                    </Field>}
                    <div className="grid grid-cols-3 gap-3">
                      <Field label="Database"><Input value={c.database ?? ''} placeholder={driverInfo(c.driver).database} onChange={(e) => set('database', e.target.value)} /></Field>
                      <Field label="Warehouse"><Input value={c.warehouse ?? ''} placeholder="your default" onChange={(e) => set('warehouse', e.target.value)} /></Field>
                      <Field label="Role"><Input value={c.role ?? ''} placeholder="your default" onChange={(e) => set('role', e.target.value)} /></Field>
                    </div>
                    <p className="text-[12px] text-muted">Reading rows runs the warehouse, which Snowflake bills. Lists of tables, columns and keys don’t.</p>
                  </> : isInflux(c.driver) ? <>
                    <Field label="Token" hint="An admin or database token. Leave empty for a server started without authentication.">{secretInput('password', c.saved?.password, 'stored in your system keyring')}</Field>
                    <Field label="Database"><Input value={c.database ?? ''} placeholder={driverInfo(c.driver).database} onChange={(e) => set('database', e.target.value)} /></Field>
                  </> : isLibSQL(c.driver) ? (
                    <Field label="Authentication token" hint={c.driver === 'libsql' ? 'Leave empty for a server that asks for none.' : 'Made with “turso db tokens create <database>”, or in the Turso dashboard.'}>{secretInput('password', c.saved?.password, 'stored in your system keyring')}</Field>
                  ) : <>
                  <div className="grid grid-cols-2 gap-3">
                    <Field label={isDynamo(c.driver) ? 'Access key ID' : 'User'}><Input value={c.user ?? ''} placeholder={driverInfo(c.driver).user} onChange={(e) => set('user', e.target.value)} /></Field>
                    <Field label={isDynamo(c.driver) ? 'Secret access key' : 'Password'}>{secretInput('password', c.saved?.password, 'stored in your system keyring')}</Field>
                  </div>
                  {!isElastic(c.driver) && <Field label={isDynamo(c.driver) ? 'Region' : 'Database'}><Input value={c.database ?? ''} placeholder={driverInfo(c.driver).database ?? 'optional'} onChange={(e) => set('database', e.target.value)} /></Field>}
                  </>}
                  {isElastic(c.driver) && <p className="text-[12px] text-muted">For an API key, leave User empty and enter the encoded key as the password.</p>}
                  {isDynamo(c.driver) && (
                    <Field label="Options" hint="Without an access key, GoTable signs in as the AWS CLI does (environment, ~/.aws, SSO); profile=name picks a profile. Reading tables on AWS is billed to the account.">
                      <Input mono value={c.options ?? ''} placeholder="profile=default" onChange={(e) => set('options', e.target.value)} />
                    </Field>
                  )}
                  {isMongo(c.driver) && (
                    <>
                      <Switch checked={!!c.srv} onChange={(v) => setC((x) => ({ ...x, srv: v, ssl_mode: v && x.ssl_mode === 'disable' ? 'require' : x.ssl_mode }))} label="DNS seed list (mongodb+srv://, as on MongoDB Atlas)" />
                      <Field label="Options" hint="The user is looked up in admin unless authSource says otherwise. A replica set can be listed in Host as a:27017,b:27017.">
                        <Input mono value={c.options ?? ''} placeholder="authSource=admin&replicaSet=rs0" onChange={(e) => set('options', e.target.value)} />
                      </Field>
                    </>
                  )}
                </>
              )}
            </Section>

            {network && (
              <Section title="Network" hint="Encryption, and a way in through another machine.">
                <div className="divide-y divide-line-2 rounded-xl border border-line">
                  <details className="group">
                    <summary className="flex cursor-pointer list-none items-center gap-2.5 px-3.5 py-3 text-[13px] font-medium">
                      <Shield className="size-4 text-muted" aria-hidden="true" /> Security (SSL)
                      <span className="ml-auto text-[12px] font-normal text-muted">{c.ssl_mode}</span>
                      <ChevronDown className="size-4 shrink-0 text-muted transition-transform duration-150 group-open:rotate-180" aria-hidden="true" />
                    </summary>
                    <div className="space-y-3 px-3.5 pb-4">
                      <Field label="SSL mode">
                        <Select value={c.ssl_mode} onChange={(e) => set('ssl_mode', e.target.value)}>
                          {forDriver(SSL_MODES, c.driver).map((m) => <option key={m}>{m}</option>)}
                        </Select>
                      </Field>
                      <div className="grid grid-cols-3 gap-3">
                        <Field label="CA certificate"><Input mono value={c.ssl_ca_path ?? ''} placeholder="path" onChange={(e) => set('ssl_ca_path', e.target.value)} /></Field>
                        <Field label="Client certificate"><Input mono value={c.ssl_cert_path ?? ''} placeholder="path" onChange={(e) => set('ssl_cert_path', e.target.value)} /></Field>
                        <Field label="Client key"><Input mono value={c.ssl_key_path ?? ''} placeholder="path" onChange={(e) => set('ssl_key_path', e.target.value)} /></Field>
                      </div>
                    </div>
                  </details>
                  <div className="px-3.5 py-3">
                    <div className="flex items-center gap-2.5">
                      <Route className="size-4 shrink-0 text-muted" aria-hidden="true" />
                      <label htmlFor="ssh-switch" className="flex-1 cursor-pointer text-[13px] font-medium">Connect through an SSH tunnel</label>
                      <Switch id="ssh-switch" checked={!!c.use_ssh_tunnel} onChange={(v) => set('use_ssh_tunnel', v)} />
                    </div>
                {c.use_ssh_tunnel && (
                  <div className="mt-3.5 space-y-3 animate-fade">
                    <div className="grid grid-cols-[1fr_110px] gap-3">
                      <Field label="SSH host"><Input value={c.ssh_host ?? ''} placeholder="bastion.example.com" onChange={(e) => set('ssh_host', e.target.value)} /></Field>
                      <Field label="SSH port"><Input inputMode="numeric" value={c.ssh_port || ''} onChange={(e) => set('ssh_port', Number(e.target.value.replace(/\D/g, '')) || 0)} /></Field>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <Field label="SSH user"><Input value={c.ssh_user ?? ''} placeholder="ubuntu" onChange={(e) => set('ssh_user', e.target.value)} /></Field>
                      <Field label="SSH password">{secretInput('sshPassword', c.saved?.sshPassword, 'or use a private key')}</Field>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <Field label="Private key" hint={keyHint}>
                        <div className="flex gap-2">
                          <Input mono icon={<KeyRound />} className="min-w-0 flex-1" value={c.ssh_key_path ?? ''} placeholder="~/.ssh/id_ed25519" aria-invalid={keyCheck?.path === keyPath && !!keyCheck.problem}
                            onChange={(e) => set('ssh_key_path', e.target.value)} />
                          {native && <Button type="button" className="w-8 shrink-0 !px-0" icon={<FolderOpen className="size-[18px]" />} onClick={pickKey}
                            aria-label="Choose the key file" title="Choose the key file" />}
                        </div>
                      </Field>
                      <Field label="Key passphrase">{secretInput('sshPassphrase', c.saved?.sshPassphrase, 'if the key is encrypted')}</Field>
                    </div>
                  </div>
                )}
                  </div>
                </div>
              </Section>
            )}
          </div>
        </div>

        {/* Always in view: the result of a test is next to the button that ran it. */}
        <div data-bottom-bar className="shrink-0 border-t border-line bg-panel-2">
          {test.kind !== 'idle' && (
            <div role="status" className={clsx('flex max-h-40 gap-2.5 overflow-y-auto border-b px-6 py-2.5 text-[12.5px] animate-fade max-[1080px]:px-4',
              test.kind === 'ok' && 'border-ok/25 bg-ok/8', test.kind === 'err' && 'border-err/25 bg-err/8', test.kind === 'running' && 'border-line')}>
              {test.kind === 'running' && <><PlugZap className="size-4 animate-pulse text-accent" /> Connecting…</>}
              {test.kind === 'ok' && <><CheckCircle2 className="mt-px size-4 shrink-0 text-ok" /><div className="flex flex-wrap gap-x-2"><span className="font-medium">Connected successfully</span><span className="text-muted">{test.text}</span></div></>}
              {test.kind === 'err' && <><XCircle className="mt-px size-4 shrink-0 text-err" /><div className="min-w-0"><div className="font-medium">{test.title}</div>
                {test.hint && <div className="text-fg-2">{test.hint}</div>}<div className="mt-1 select-text break-words font-mono text-[11.5px] text-muted">{test.detail}</div></div></>}
            </div>
          )}
          <div className="flex items-center gap-2 px-6 py-3 max-[1080px]:px-4">
            <Button type="button" onClick={doTest} loading={test.kind === 'running'} icon={<PlugZap className="size-3.5" />}>Test</Button>
            <div className="min-w-0 flex-1 truncate pl-1 text-[12px] text-muted">{!isNew && dirty && 'Not saved yet'}</div>
            {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
            <Button type="button" onClick={() => save(false)} disabled={saving || (!dirty && !isNew)}>Save</Button>
            <Button type="submit" variant="primary" loading={saving || connecting}>{connecting ? 'Connecting…' : dirty || isNew ? 'Save & connect' : 'Connect'}</Button>
          </div>
        </div>
      </div>

      {urlOpen && <UrlDialog onClose={() => setUrlOpen(false)} onParsed={(conn, pw) => {
        setC((x) => ({ ...x, ...conn, id: x.id, group_id: x.group_id, tag: x.tag, name: x.name || conn.name }))
        if (pw) setSecrets((s) => ({ ...s, password: pw }))
        setUrlOpen(false)
      }} />}
    </form>
  )
}

export function UrlDialog({ onClose, onParsed }: { onClose: () => void; onParsed: (c: Connection, password: string) => void }) {
  const [url, setUrl] = useState('')
  const [err, setErr] = useState('')
  const go = async () => {
    try {
      const r = await api.parseURL(url)
      onParsed(r.connection, r.password)
    } catch (e) {
      setErr(errorParts(e)[1] || errorParts(e)[0])
    }
  }
  return (
    <Modal title="Paste a connection URL" subtitle="postgres://, mysql://, sqlserver://, clickhouse://, mongodb://, sqlite:///path, duckdb:///path… (Neon, Supabase and PlanetScale are recognised by their host) — the password goes to your keyring." onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={go} disabled={!url.trim()}>Fill in</Button></>}>
      <Input autoFocus mono value={url} placeholder="postgres://user:password@localhost:5432/shop" onChange={(e) => { setUrl(e.target.value); setErr('') }}
        onKeyDown={(e) => e.key === 'Enter' && go()} />
      {err && <div className="mt-2 text-[12px] text-err">{err}</div>}
    </Modal>
  )
}
