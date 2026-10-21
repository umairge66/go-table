import clsx from 'clsx'
import { CheckCircle2, ChevronDown, ClipboardPaste, FolderOpen, KeyRound, Lock, PlugZap, Shield, XCircle } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { api, ApiError } from '../api'
import { Button, DriverBadge, Field, Input, Modal, Select, Switch, errorParts, useToast } from '../components/ui'
import { DRIVERS, SSL_MODES, TAGS, driverInfo, type Connection, type Driver, type Group, type Secrets } from '../types'
import { nativeDialogs, openFileDialog } from '../lib/native'
import { examplePath } from '../lib/platform'

export const newConnection = (driver: Driver = 'postgres'): Connection => ({
  id: crypto.randomUUID().replace(/-/g, '').toUpperCase(),
  name: '',
  driver,
  host: '127.0.0.1',
  port: driverInfo(driver).port,
  user: '',
  database: '',
  ssl_mode: SSL_MODES[driver][0],
  ssh_port: 22,
  rows_per_page: 300,
  tag: '',
})

type TestState = { kind: 'idle' } | { kind: 'running' } | { kind: 'ok'; text: string } | { kind: 'err'; title: string; hint?: string; detail: string }

export function ConnectionForm({ initial, initialPassword, groups, isNew, onSaved, onConnect, onCancel, connecting }: {
  initial: Connection
  initialPassword?: string
  groups: Group[]
  isNew: boolean
  onSaved: (c: Connection) => void
  onConnect: (c: Connection) => void
  onCancel?: () => void
  /** connecting: this connection is being opened (SSH can take a while). */
  connecting?: boolean
}) {
  const toast = useToast()
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
  const file = c.driver === 'sqlite'
  const dirty = useMemo(() => JSON.stringify(c) !== JSON.stringify(initial) || Object.keys(secrets).length > 0, [c, initial, secrets])

  const setDriver = (d: Driver) => setC((x) => {
    const prevDefault = driverInfo(x.driver).port
    return {
      ...x, driver: d,
      port: !x.port || x.port === prevDefault ? driverInfo(d).port : x.port,
      ssl_mode: SSL_MODES[d].includes(x.ssl_mode ?? '') ? x.ssl_mode : SSL_MODES[d][0],
    }
  })

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
      className="mx-auto w-full max-w-[600px] animate-fade"
      onSubmit={(e) => { e.preventDefault(); save(true) }}
      aria-label="Connection form"
    >
      <div className="mb-5 flex items-center gap-3">
        <DriverBadge driver={c.driver} size={42} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[18px] font-semibold">{c.name || (isNew ? 'New connection' : 'Untitled')}</div>
          <div className="text-[12.5px] text-muted">{isNew ? 'Choose a database, then tell GoTable where to find it.' : 'Edit the connection, test it, then save.'}</div>
        </div>
        <Button type="button" variant="ghost" size="sm" icon={<ClipboardPaste className="size-3.5" />} onClick={() => setUrlOpen(true)}>Paste URL</Button>
      </div>

      <div className="mb-4 grid grid-cols-4 gap-2" role="radiogroup" aria-label="Database type">
        {DRIVERS.map((d) => (
          <button
            key={d.id}
            type="button"
            role="radio"
            aria-checked={c.driver === d.id}
            onClick={() => setDriver(d.id)}
            className={clsx('flex flex-col items-center gap-1.5 rounded-xl border bg-panel py-3 text-[12.5px] font-medium transition-all',
              c.driver === d.id ? 'border-accent ring-3 ring-accent/15' : 'border-line hover:border-faint')}
          >
            <DriverBadge driver={d.id} size={28} decorative />
            {d.label}
          </button>
        ))}
      </div>

      <section className="space-y-3 rounded-xl border border-line bg-panel p-4">
        <div className="grid grid-cols-[1fr_auto] gap-3">
          <Field label="Name"><Input autoFocus value={c.name} placeholder={file ? 'Taken from the file name if empty' : 'e.g. Shop production'} onChange={(e) => set('name', e.target.value)} /></Field>
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
                  (c.tag ?? '') === t.id ? 'border-transparent font-medium text-white' : 'border-line hover:bg-hover')}
                style={(c.tag ?? '') === t.id ? { background: t.color } : undefined}
              >
                <span className="size-2 rounded-full" style={{ background: (c.tag ?? '') === t.id ? 'white' : t.color }} />
                {t.label}
              </button>
            ))}
          </div>
        </Field>
      </section>

      <section className="mt-3 space-y-3 rounded-xl border border-line bg-panel p-4">
        {file ? (
          <Field label="Database file" hint="Absolute path. A new file is created if it doesn't exist.">
            <Input mono value={c.path ?? ''} placeholder={examplePath('data/app.sqlite')} onChange={(e) => set('path', e.target.value)} />
          </Field>
        ) : (
          <>
            <div className="grid grid-cols-[1fr_110px] gap-3">
              <Field label="Host"><Input value={c.host ?? ''} placeholder="127.0.0.1" onChange={(e) => set('host', e.target.value)} /></Field>
              <Field label="Port"><Input inputMode="numeric" value={c.port || ''} onChange={(e) => set('port', Number(e.target.value.replace(/\D/g, '')) || 0)} /></Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label="User"><Input value={c.user ?? ''} placeholder={c.driver === 'postgres' ? 'postgres' : 'root'} onChange={(e) => set('user', e.target.value)} /></Field>
              <Field label="Password">{secretInput('password', c.saved?.password, 'stored in your system keyring')}</Field>
            </div>
            <Field label="Database"><Input value={c.database ?? ''} placeholder="optional" onChange={(e) => set('database', e.target.value)} /></Field>
          </>
        )}
      </section>

      {!file && (
        <details className="group mt-3 rounded-xl border border-line bg-panel">
          <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 text-[13px] font-medium">
            <Shield className="size-4 text-muted" /> Security (SSL)
            <span className="ml-auto text-[12px] font-normal text-muted">{c.ssl_mode}</span>
            <ChevronDown className="size-4 shrink-0 text-muted transition-transform duration-150 group-open:rotate-180" aria-hidden="true" />
          </summary>
          <div className="space-y-3 px-4 pb-4">
            <Field label="SSL mode">
              <Select value={c.ssl_mode} onChange={(e) => set('ssl_mode', e.target.value)}>
                {SSL_MODES[c.driver].map((m) => <option key={m}>{m}</option>)}
              </Select>
            </Field>
            <div className="grid grid-cols-3 gap-3">
              <Field label="CA certificate"><Input mono value={c.ssl_ca_path ?? ''} placeholder="path" onChange={(e) => set('ssl_ca_path', e.target.value)} /></Field>
              <Field label="Client certificate"><Input mono value={c.ssl_cert_path ?? ''} placeholder="path" onChange={(e) => set('ssl_cert_path', e.target.value)} /></Field>
              <Field label="Client key"><Input mono value={c.ssl_key_path ?? ''} placeholder="path" onChange={(e) => set('ssl_key_path', e.target.value)} /></Field>
            </div>
          </div>
        </details>
      )}

      {!file && (
        <section className="mt-3 rounded-xl border border-line bg-panel p-4">
          <Switch checked={!!c.use_ssh_tunnel} onChange={(v) => set('use_ssh_tunnel', v)} label={<span className="font-medium">Connect through an SSH tunnel</span>} />
          {c.use_ssh_tunnel && (
            <div className="mt-3 space-y-3 animate-fade">
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
        </section>
      )}

      {test.kind !== 'idle' && (
        <div role="status" className={clsx('mt-3 flex gap-2.5 rounded-xl border p-3 text-[12.5px] animate-fade',
          test.kind === 'ok' && 'border-ok/30 bg-ok/8', test.kind === 'err' && 'border-err/30 bg-err/8', test.kind === 'running' && 'border-line bg-panel')}>
          {test.kind === 'running' && <><PlugZap className="size-4 animate-pulse text-accent" /> Connecting…</>}
          {test.kind === 'ok' && <><CheckCircle2 className="size-4 shrink-0 text-ok" /><div><div className="font-medium">Connected successfully</div><div className="text-muted">{test.text}</div></div></>}
          {test.kind === 'err' && <><XCircle className="size-4 shrink-0 text-err" /><div className="min-w-0"><div className="font-medium">{test.title}</div>
            {test.hint && <div className="text-fg-2">{test.hint}</div>}<div className="mt-1 break-words font-mono text-[11.5px] text-muted">{test.detail}</div></div></>}
        </div>
      )}

      <div className="sticky bottom-0 mt-4 flex items-center gap-2 bg-gradient-to-t from-bg via-bg pb-4 pt-3">
        <Button type="button" onClick={doTest} loading={test.kind === 'running'} icon={<PlugZap className="size-3.5" />}>Test</Button>
        <div className="flex-1" />
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
        <Button type="button" onClick={() => save(false)} disabled={saving || (!dirty && !isNew)}>Save</Button>
        <Button type="submit" variant="primary" loading={saving || connecting}>{connecting ? 'Connecting…' : dirty || isNew ? 'Save & connect' : 'Connect'}</Button>
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
    <Modal title="Paste a connection URL" subtitle="postgres://, mysql://, mariadb:// or sqlite:///path — the password goes to your keyring." onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={go} disabled={!url.trim()}>Fill in</Button></>}>
      <Input autoFocus mono value={url} placeholder="postgres://user:password@localhost:5432/shop" onChange={(e) => { setUrl(e.target.value); setErr('') }}
        onKeyDown={(e) => e.key === 'Enter' && go()} />
      {err && <div className="mt-2 text-[12px] text-err">{err}</div>}
    </Modal>
  )
}
