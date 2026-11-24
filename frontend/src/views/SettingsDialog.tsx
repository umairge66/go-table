import clsx from 'clsx'
import { AlertTriangle, ArrowUpCircle, Check, CheckCircle2, Columns2, ExternalLink, RefreshCw, Keyboard, Monitor, Moon, PanelLeft, PanelRight, PanelsTopLeft, Palette, RotateCcw, Search, SquareCode, Sun, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { SqlEditor } from '../components/SqlEditor'
import { Button, Input, Kbd, Modal } from '../components/ui'
import { ACCENTS, DEFAULTS, FONT_SIZES, accentColors, flushSettings, updateSettings, useSettings, type Panel, type PanelChoice, type ThemeChoice } from '../lib/settings'
import { KIND_LABELS, checkForUpdates, installUpdate, openRelease, plainVersion, restartToUpdate, unskipUpdate, useUpdates } from '../lib/updates'
import { Notes } from '../components/UpdateNotice'
import { FIXED, RESERVED, SHORTCUTS, binding, comboOf, conflicts, def, label, type ShortcutDef } from '../lib/shortcuts'

type Section = 'appearance' | 'panels' | 'editor' | 'keyboard' | 'updates'

const SECTIONS: [Section, string, React.ReactElement][] = [
  ['appearance', 'Appearance', <Palette key="a" />],
  ['panels', 'Panels', <PanelsTopLeft key="p" />],
  ['editor', 'Editor', <SquareCode key="e" />],
  ['keyboard', 'Keyboard shortcuts', <Keyboard key="k" />],
  ['updates', 'Updates', <RefreshCw key="u" />],
]

/** SettingsDialog: the app's preferences. Changes apply at once and are saved as they are made. */
export function SettingsDialog({ onClose }: { onClose: () => void }) {
  const [section, setSection] = useState<Section>('appearance')
  const close = () => { flushSettings(); onClose() }
  return (
    <Modal width={860} title="Settings" subtitle="Changes apply at once and are kept for the next launch." onClose={close}
      footer={<Button variant="primary" onClick={close}>Done</Button>}>
      <div className="flex min-h-[460px] gap-5">
        <nav className="w-48 shrink-0 space-y-0.5" aria-label="Settings sections">
          {SECTIONS.map(([id, text, icon]) => (
            <button key={id} onClick={() => setSection(id)} aria-current={section === id ? 'page' : undefined}
              className={clsx('flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-left text-[13px] [&>svg]:size-4',
                section === id ? 'bg-active font-medium text-fg' : 'text-fg-2 hover:bg-hover')}>
              {icon}{text}
            </button>
          ))}
        </nav>
        <div className="min-w-0 flex-1">
          {section === 'appearance' && <Appearance />}
          {section === 'panels' && <Panels />}
          {section === 'editor' && <Editor />}
          {section === 'keyboard' && <Shortcuts />}
          {section === 'updates' && <Updates />}
        </div>
      </div>
    </Modal>
  )
}

function Group({ title, hint, children }: { title: string; hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="mb-6" aria-label={title}>
      <h3 className="text-[13px] font-semibold">{title}</h3>
      {hint && <p className="mt-0.5 text-[12.5px] text-muted">{hint}</p>}
      <div className="mt-2.5">{children}</div>
    </section>
  )
}

// ---------------------------------------------------------------- appearance

function Appearance() {
  const s = useSettings()
  const custom = !ACCENTS.some((a) => a.id === s.accent)
  const [hex, setHex] = useState(custom ? s.accent : '#0ea5e9')
  useEffect(() => { if (custom) setHex(s.accent) }, [s.accent, custom])
  const themes: [ThemeChoice, string, React.ReactElement][] = [['system', 'System', <Monitor key="s" />], ['light', 'Light', <Sun key="l" />], ['dark', 'Dark', <Moon key="d" />]]
  return (
    <>
      <Group title="Theme" hint="System follows your computer's light or dark mode.">
        <div className="inline-flex rounded-lg border border-line bg-panel-2 p-0.5" role="radiogroup" aria-label="Theme">
          {themes.map(([id, text, icon]) => (
            <button key={id} role="radio" aria-checked={s.theme === id} onClick={() => updateSettings({ theme: id })}
              className={clsx('flex h-8 items-center gap-1.5 rounded-md px-3.5 text-[13px] [&>svg]:size-3.5',
                s.theme === id ? 'bg-panel font-medium text-fg shadow-[0_0_0_1px_var(--line)]' : 'text-muted hover:text-fg')}>
              {icon}{text}
            </button>
          ))}
        </div>
      </Group>
      <Group title="Accent colour" hint="Buttons, selections, the cursor and highlights. Each colour has a shade for dark mode.">
        <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Accent colour">
          {ACCENTS.map((a) => (
            <Swatch key={a.id} name={a.label} light={a.light} dark={a.dark} selected={s.accent === a.id} onPick={() => updateSettings({ accent: a.id })} />
          ))}
          <label className={clsx('flex h-9 cursor-pointer items-center gap-2 rounded-lg border px-2 text-[12.5px]',
            custom ? 'border-accent ring-3 ring-accent/20' : 'border-line hover:border-faint')} title="Pick any colour">
            <input type="color" aria-label="Custom accent colour" value={hex} className="size-6 cursor-pointer rounded border-0 bg-transparent p-0"
              onChange={(e) => { setHex(e.target.value); updateSettings({ accent: e.target.value }) }} />
            Custom
            {custom && <code className="font-mono text-[11.5px] text-muted">{s.accent}</code>}
          </label>
        </div>
      </Group>
      <Group title="Preview">
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-line bg-panel-2 p-4" data-testid="accent-preview">
          <Button variant="primary" icon={<Check className="size-3.5" />}>Commit</Button>
          <span className="rounded-md bg-selected px-2 py-1 text-[12.5px]">Selected row</span>
          <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[11.5px] font-semibold text-accent">UNIQUE</span>
          <span className="text-[13px] font-medium text-accent">A link</span>
          <span className="inline-block h-5 w-0.5 animate-pulse bg-accent" aria-hidden />
        </div>
        {s.accent !== DEFAULTS.accent && (
          <button className="mt-2 flex items-center gap-1 text-[12px] text-muted hover:text-fg" onClick={() => updateSettings({ accent: DEFAULTS.accent })}>
            <RotateCcw className="size-3" /> Back to Indigo, the default
          </button>
        )}
      </Group>
    </>
  )
}

function Swatch({ name, light, dark, selected, onPick }: { name: string; light: string; dark: string; selected: boolean; onPick: () => void }) {
  const { lightFg } = accentColors(light)
  return (
    <button role="radio" aria-checked={selected} aria-label={name} title={name} onClick={onPick}
      className={clsx('relative flex size-9 items-center justify-center overflow-hidden rounded-lg border',
        selected ? 'border-fg ring-3 ring-accent/25' : 'border-line hover:scale-105')}>
      {/* Half the light theme's shade, half the dark one's. An SVG rather than
          a gradient: a gradient's hard stop isn't anti-aliased, so its
          diagonal comes out jagged. The dark shade fills it and the light
          triangle lies on top, so there's no seam between the halves. */}
      <svg className="absolute inset-0 size-full" viewBox="0 0 36 36" preserveAspectRatio="none" aria-hidden>
        <rect width="36" height="36" fill={dark} />
        <polygon points="0,0 36,0 0,36" fill={light} />
      </svg>
      {selected && <Check className="relative size-4" style={{ color: lightFg }} strokeWidth={3} />}
    </button>
  )
}

// ---------------------------------------------------------------- panels

const PANEL_INFO: [Panel, string, string, string, React.ReactElement][] = [
  ['rail', 'Database rail', 'The narrow column of open databases on the far left.', 'ws.rail', <Columns2 key="r" />],
  ['sidebar', 'Sidebar', 'The tables, saved queries and history.', 'ws.sidebar', <PanelLeft key="s" />],
  ['inspector', 'Row inspector', 'The selected row’s fields, on the right of a table.', 'ws.inspector', <PanelRight key="i" />],
]

function Panels() {
  const s = useSettings()
  const choices: [PanelChoice, string][] = [['open', 'Open'], ['closed', 'Closed'], ['last', 'As I left it']]
  return (
    <Group title="When a connection opens" hint="How each panel starts. A change also applies to the connections open now. You can still show or hide it at any time with its button or keys.">
      <div className="overflow-hidden rounded-xl border border-line">
        {PANEL_INFO.map(([p, name, what, shortcut, icon]) => (
          <div key={p} className="flex items-center gap-3 border-b border-line-2 px-3 py-2.5 last:border-0" data-testid={`panel-${p}`}>
            <span className="text-muted [&>svg]:size-4">{icon}</span>
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium">{name}</div>
              <div className="text-[12px] text-muted">
                {what} {label(binding(shortcut, s.shortcuts)) && <>Toggle: <Kbd>{label(binding(shortcut, s.shortcuts))}</Kbd></>}
                {s.panels[p] === 'last' && <> · now {s.lastPanels[p] ? 'open' : 'closed'}</>}
              </div>
            </div>
            <div className="inline-flex shrink-0 rounded-lg border border-line bg-panel-2 p-0.5" role="radiogroup" aria-label={name}>
              {choices.map(([c, text]) => (
                <button key={c} role="radio" aria-checked={s.panels[p] === c} onClick={() => updateSettings({ panels: { ...s.panels, [p]: c } })}
                  className={clsx('h-7 rounded-md px-2.5 text-[12.5px]', s.panels[p] === c ? 'bg-panel font-medium text-fg shadow-[0_0_0_1px_var(--line)]' : 'text-muted hover:text-fg')}>
                  {text}
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </Group>
  )
}

// ---------------------------------------------------------------- editor

const SAMPLE = `-- The text size every query tab starts with
SELECT name, email, created_at
FROM customers
WHERE created_at > now() - interval '30 days'
ORDER BY created_at DESC;`

function Editor() {
  const s = useSettings()
  const zoomIn = label(binding('editor.zoomIn', s.shortcuts))
  const zoomOut = label(binding('editor.zoomOut', s.shortcuts))
  const reset = label(binding('editor.zoomReset', s.shortcuts))
  const set = (n: number) => updateSettings({ editorFontSize: n })
  return (
    <>
      <Group title="Default text size" hint={<>
        The query editor's text size, in pixels. A query tab can make its own text larger or smaller
        {zoomIn || zoomOut ? <> with <Kbd>{zoomIn}</Kbd> / <Kbd>{zoomOut}</Kbd></> : ''} or the A− A+ buttons{reset ? <>; <Kbd>{reset}</Kbd> goes back to this size</> : ''}.
      </>}>
        <div className="flex items-center gap-3">
          <Button size="sm" aria-label="Smaller default text" disabled={s.editorFontSize <= FONT_SIZES.min} onClick={() => set(s.editorFontSize - 1)}>A−</Button>
          <input type="range" min={FONT_SIZES.min} max={FONT_SIZES.max} value={s.editorFontSize} aria-label="Default text size"
            className="w-56 accent-[var(--accent)]" onChange={(e) => set(Number(e.target.value))} />
          <Button size="sm" aria-label="Larger default text" disabled={s.editorFontSize >= FONT_SIZES.max} onClick={() => set(s.editorFontSize + 1)}>A+</Button>
          <span className="w-12 text-[13px] font-semibold tabular-nums" data-testid="default-font-size">{s.editorFontSize} px</span>
          {s.editorFontSize !== DEFAULTS.editorFontSize && (
            <button className="flex items-center gap-1 text-[12px] text-muted hover:text-fg" onClick={() => set(DEFAULTS.editorFontSize)}>
              <RotateCcw className="size-3" /> {DEFAULTS.editorFontSize} px
            </button>
          )}
        </div>
        <div className="mt-3 h-[170px] overflow-hidden rounded-lg border border-line" data-testid="font-preview">
          <SqlEditor value={SAMPLE} driver="postgres" readOnly />
        </div>
      </Group>
    </>
  )
}

// ---------------------------------------------------------------- shortcuts

function Shortcuts() {
  const s = useSettings()
  const [search, setSearch] = useState('')
  const [recording, setRecording] = useState<string | null>(null)
  const [problem, setProblem] = useState<{ id: string; text: string } | null>(null)
  const changed = SHORTCUTS.filter((d) => s.shortcuts[d.id] !== undefined && s.shortcuts[d.id] !== d.default)
  const q = search.trim().toLowerCase()
  const shown = SHORTCUTS.filter((d) => !q || d.label.toLowerCase().includes(q) || d.group.toLowerCase().includes(q) || label(binding(d.id, s.shortcuts)).toLowerCase().includes(q))
  const groups = useMemo(() => [...new Set(SHORTCUTS.map((d) => d.group))], [])

  const set = (id: string, combo: string | undefined) => {
    const next = { ...s.shortcuts }
    if (combo === undefined || combo === def(id)?.default) delete next[id]
    else next[id] = combo
    updateSettings({ shortcuts: next })
  }

  return (
    <>
      <div className="mb-3 flex items-center gap-2">
        <Input icon={<Search className="size-3.5" />} className="flex-1" placeholder="Search shortcuts or keys" aria-label="Search shortcuts" value={search} onChange={(e) => setSearch(e.target.value)} />
        <Button size="sm" variant="ghost" icon={<RotateCcw className="size-3.5" />} disabled={!changed.length && !Object.keys(s.shortcuts).length}
          onClick={() => { updateSettings({ shortcuts: {} }); setProblem(null) }}>Reset all</Button>
      </div>
      <p className="mb-3 text-[12.5px] text-muted">Click a shortcut to record new keys. Ctrl also stands for ⌘ on a Mac.</p>
      {groups.map((g) => {
        const rows = shown.filter((d) => d.group === g)
        if (!rows.length) return null
        return (
          <section key={g} className="mb-4" aria-label={g}>
            <h3 className="mb-1.5 text-[11.5px] font-semibold uppercase tracking-wider text-muted">{g}</h3>
            <div className="overflow-hidden rounded-xl border border-line">
              {rows.map((d) => (
                <ShortcutRow key={d.id} d={d} overrides={s.shortcuts} recording={recording === d.id}
                  problem={problem?.id === d.id ? problem.text : ''}
                  onRecord={() => { setRecording(d.id); setProblem(null) }}
                  onCancel={() => setRecording(null)}
                  onRecorded={(combo) => {
                    setRecording(null)
                    if (RESERVED.includes(combo)) { setProblem({ id: d.id, text: `${label(combo)} is kept for its usual job (copy, undo, tabs…). Pick other keys.` }); return }
                    setProblem(null)
                    set(d.id, combo)
                  }}
                  onReset={() => set(d.id, undefined)}
                  onClear={() => set(d.id, '')} />
              ))}
            </div>
          </section>
        )
      })}
      {!shown.length && <div className="py-6 text-center text-[12.5px] text-muted">No shortcut matches “{search}”.</div>}
      {!q && (
        <section aria-label="Fixed shortcuts">
          <h3 className="mb-1.5 text-[11.5px] font-semibold uppercase tracking-wider text-muted">Fixed</h3>
          <div className="overflow-hidden rounded-xl border border-line">
            {FIXED.map(([k, what]) => (
              <div key={k} className="flex items-center gap-3 border-b border-line-2 px-3 py-2 text-[12.5px] last:border-0">
                <span className="flex-1 text-fg-2">{what}</span><Kbd>{k}</Kbd>
              </div>
            ))}
          </div>
        </section>
      )}
    </>
  )
}

function ShortcutRow({ d, overrides, recording, problem, onRecord, onCancel, onRecorded, onReset, onClear }: {
  d: ShortcutDef; overrides: Record<string, string>; recording: boolean; problem: string
  onRecord: () => void; onCancel: () => void; onRecorded: (combo: string) => void; onReset: () => void; onClear: () => void
}) {
  const keys = binding(d.id, overrides)
  const isChanged = overrides[d.id] !== undefined && overrides[d.id] !== d.default
  const clash = conflicts(d.id, keys, overrides)
  const button = useRef<HTMLButtonElement>(null)

  // While recording, the next key press is the shortcut, and nothing else sees it.
  useEffect(() => {
    if (!recording) return
    button.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      e.preventDefault()
      e.stopImmediatePropagation()
      if (e.key === 'Escape' && !e.ctrlKey && !e.altKey && !e.shiftKey && !e.metaKey) { onCancel(); return }
      const combo = comboOf(e)
      if (combo) onRecorded(combo)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [recording, onCancel, onRecorded])

  return (
    <div className={clsx('border-b border-line-2 px-3 py-2 last:border-0', recording && 'bg-accent-soft')} data-testid={`shortcut-${d.id}`}>
      <div className="flex items-center gap-3">
        <span className="flex-1 text-[13px]">{d.label}</span>
        {isChanged && <span className="text-[11px] text-muted" title={`Default: ${label(d.default)}`}>changed</span>}
        <button ref={button} data-captures-keys={recording ? '' : undefined} onClick={recording ? onCancel : onRecord}
          aria-label={recording ? `Press the new keys for ${d.label}` : `Change the keys for ${d.label}`}
          className={clsx('flex h-7 min-w-28 items-center justify-center rounded-md border px-2 text-[12px]',
            recording ? 'border-accent text-accent' : 'border-line hover:border-faint', clash.length > 0 && !recording && 'border-warn')}>
          {recording ? 'Press keys… (Esc cancels)' : keys ? <Kbd>{label(keys)}</Kbd> : <span className="text-faint">None</span>}
        </button>
        <span className="flex w-14 justify-end gap-0.5">
          {isChanged && <button className="rounded p-1 text-muted hover:bg-hover hover:text-fg" aria-label={`Reset ${d.label}`} title={`Back to ${label(d.default)}`} onClick={onReset}><RotateCcw className="size-3.5" /></button>}
          {keys && <button className="rounded p-1 text-muted hover:bg-hover hover:text-fg" aria-label={`Remove the keys of ${d.label}`} title="No keys" onClick={onClear}><X className="size-3.5" /></button>}
        </span>
      </div>
      {clash.length > 0 && !recording && (
        <div className="mt-1 flex items-center gap-1 text-[12px] text-warn" role="alert">
          <AlertTriangle className="size-3.5 shrink-0" /> Also {clash.map((c) => `“${c.label}”`).join(', ')}: only one of them will work. Change one.
        </div>
      )}
      {problem && <div className="mt-1 text-[12px] text-err" role="alert">{problem}</div>}
    </div>
  )
}

// ---------------------------------------------------------------- updates

function Updates() {
  const s = useSettings()
  const u = useUpdates()
  const dev = !u.info || u.info.how === 'none'
  const ago = (t: number) => {
    if (!t) return 'never'
    const m = Math.round((Date.now() - t) / 60000)
    return m < 1 ? 'just now' : m < 60 ? `${m} min ago` : m < 48 * 60 ? `${Math.round(m / 60)} h ago` : `${Math.round(m / 1440)} days ago`
  }
  const busy = u.progress.phase === 'downloading' || u.progress.phase === 'installing'
  return (
    <>
      <Group title="This copy">
        <div className="rounded-xl border border-line px-3 py-2.5 text-[13px]" data-testid="update-version">
          GoTable <b className="font-semibold">{dev ? (u.info?.version ?? 'dev') : plainVersion(u.info?.version)}</b>
          <span className="text-muted"> · {u.info?.kind === 'windows' ? (u.info.installed ? 'installed on Windows' : 'portable Windows program') : KIND_LABELS[u.info?.kind ?? 'dev'] ?? u.info?.kind}</span>
          {u.info?.how === 'installer' && <div className="mt-1 text-[12px] text-muted">Installed by your system, so an update is downloaded and opened with its installer.</div>}
          {dev && <div className="mt-1 text-[12px] text-muted">A development build isn’t updated from GitHub.</div>}
        </div>
      </Group>
      {!dev && (
        <Group title="Updates" hint="GoTable asks GitHub for its newest release. Nothing about you or your databases is sent. An update is installed only if it is signed with GoTable’s release key.">
          <div className="space-y-3">
            <label className="flex items-center gap-2.5 text-[13px]">
              <input type="checkbox" className="size-4 accent-[var(--accent)]" checked={s.updates.auto} aria-label="Check for updates automatically"
                onChange={(e) => updateSettings({ updates: { ...s.updates, auto: e.target.checked } })} />
              Check automatically, once a day
            </label>
            <div className="flex flex-wrap items-center gap-3">
              <Button size="sm" icon={<RefreshCw className={clsx('size-3.5', u.checking && 'animate-spin')} />} disabled={u.checking || busy} onClick={() => checkForUpdates(true)}>Check now</Button>
              <span className="text-[12.5px] text-muted" data-testid="update-status">
                {u.checking ? 'Checking…'
                  : u.checkError ? <span className="text-err">{u.checkError}</span>
                  : u.check ? (u.check.available ? <span className="font-medium text-accent">GoTable {plainVersion(u.check.latest)} is available</span> : <span className="inline-flex items-center gap-1 text-ok"><CheckCircle2 className="size-3.5" /> Up to date</span>)
                  : null}
                {!u.checking && <span> · last checked {ago(s.updates.lastCheck)}</span>}
              </span>
            </div>
            {u.check?.available && (
              <div className="rounded-xl border border-line p-3">
                <div className="flex items-center gap-2">
                  <ArrowUpCircle className="size-4 text-accent" />
                  <span className="flex-1 text-[13px] font-semibold">GoTable {plainVersion(u.check.latest)}</span>
                  <Button size="sm" variant="ghost" icon={<ExternalLink className="size-3.5" />} onClick={openRelease}>On GitHub</Button>
                  <Button size="sm" variant="primary" disabled={busy || u.restarting} onClick={u.progress.phase === 'ready' ? restartToUpdate : installUpdate}>
                    {u.progress.phase === 'ready' ? 'Restart now' : busy ? 'Installing…' : u.info?.how === 'installer' ? 'Download & install' : 'Update'}
                  </Button>
                </div>
                {u.progress.phase === 'ready' && <div className="mt-1 text-[12px] text-muted">Installed: restart to use it. Commit or discard open edits first.</div>}
                {u.check.notes && <div className="mt-2 max-h-56 overflow-auto rounded-md bg-panel-2 p-2.5"><Notes text={u.check.notes} /></div>}
              </div>
            )}
            {s.updates.skipped && (
              <div className="text-[12.5px] text-muted">
                You skipped GoTable {plainVersion(s.updates.skipped)}. <button className="font-medium text-accent hover:underline" onClick={unskipUpdate}>Show it again</button>
              </div>
            )}
          </div>
        </Group>
      )}
    </>
  )
}
