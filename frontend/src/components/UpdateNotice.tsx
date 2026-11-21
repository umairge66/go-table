import clsx from 'clsx'
import { AlertCircle, ArrowUpCircle, CheckCircle2, ExternalLink, X } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useSettings } from '../lib/settings'
import { installUpdate, laterUpdate, mb, openRelease, plainVersion, restartToUpdate, showNotice, skipUpdate, useUpdates } from '../lib/updates'
import { Button, Spinner } from './ui'

/**
 * UpdateNotice: a card in the bottom-left corner when GitHub has a newer
 * GoTable, and while one installs. Toasts keep the other corner.
 */
export function UpdateNotice() {
  const u = useUpdates()
  const skipped = useSettings().updates.skipped
  const [notes, setNotes] = useState(false)
  if (!showNotice(u, skipped)) return null
  const p = u.progress
  const latest = plainVersion(u.check?.latest ?? p.version)
  const installer = u.info?.how === 'installer'

  let body: ReactNode
  if (u.restarting) {
    body = <Head icon={<Spinner className="size-4" />} title="Restarting…" sub={`GoTable ${latest} opens in a moment.`} />
  } else if (p.phase === 'downloading' || p.phase === 'installing') {
    const pct = p.total ? Math.round((p.done / p.total) * 100) : 0
    body = (
      <>
        <Head icon={<Spinner className="size-4" />} title={p.phase === 'installing' ? `Installing GoTable ${latest}…` : `Downloading GoTable ${latest}…`}
          sub={p.phase === 'downloading' && p.total ? `${mb(p.done)} of ${mb(p.total)} MB · checked before it is installed` : 'Checking its signature, then putting it in place'} />
        <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-hover" role="progressbar" aria-valuenow={pct} aria-label="Update progress">
          <div className="h-full bg-accent transition-[width]" style={{ width: `${p.phase === 'installing' ? 100 : pct}%` }} />
        </div>
      </>
    )
  } else if (p.phase === 'ready') {
    body = (
      <>
        <Head icon={<CheckCircle2 className="size-4 text-ok" />} title={`GoTable ${latest} is installed`} sub="Restart to use it; otherwise it starts next time. Commit or discard open edits first." />
        <div className="mt-3 flex gap-2">
          <Button size="sm" variant="primary" onClick={restartToUpdate}>Restart now</Button>
          <Button size="sm" variant="ghost" onClick={laterUpdate}>Later</Button>
        </div>
      </>
    )
  } else if (p.phase === 'opened') {
    body = (
      <>
        <Head icon={<CheckCircle2 className="size-4 text-ok" />} title={`GoTable ${latest} is downloaded`} sub="Your system’s installer has it open: finish there, then restart GoTable." />
        <div className="mt-3 flex gap-2">
          <Button size="sm" variant="ghost" onClick={laterUpdate}>OK</Button>
        </div>
      </>
    )
  } else if (p.phase === 'failed') {
    body = (
      <>
        <Head icon={<AlertCircle className="size-4 text-err" />} title={p.error?.title ?? 'The update wasn’t installed'} sub={p.error?.hint} />
        {p.error?.detail && <div className="mt-2 max-h-24 overflow-auto break-words rounded-md bg-panel-2 p-2 font-mono text-[11.5px] text-muted">{p.error.detail}</div>}
        <div className="mt-3 flex flex-wrap gap-2">
          <Button size="sm" variant="primary" onClick={installUpdate}>Try again</Button>
          <Button size="sm" variant="ghost" icon={<ExternalLink className="size-3.5" />} onClick={openRelease}>Get it from GitHub</Button>
          <Button size="sm" variant="ghost" onClick={laterUpdate}>Close</Button>
        </div>
      </>
    )
  } else {
    body = (
      <>
        <Head icon={<ArrowUpCircle className="size-4 text-accent" />} title={`GoTable ${latest} is available`}
          sub={`You have ${plainVersion(u.check?.current)}.${u.check?.asset && u.check.asset.size >= 100_000 ? ` ${mb(u.check.asset.size)} MB.` : ''}`} />
        {notes && u.check?.notes && <div className="mt-2 max-h-64 overflow-auto rounded-md border border-line bg-panel-2 p-2.5" data-testid="update-notes"><Notes text={u.check.notes} /></div>}
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Button size="sm" variant="primary" onClick={installUpdate}>{installer ? 'Download & install' : 'Update'}</Button>
          <Button size="sm" variant="ghost" aria-expanded={notes} onClick={() => setNotes((v) => !v)}>What’s new</Button>
          <Button size="sm" variant="ghost" onClick={laterUpdate}>Later</Button>
          <button className="ml-auto text-[12px] text-muted hover:text-fg" onClick={skipUpdate}>Skip this version</button>
        </div>
      </>
    )
  }
  const closable = !u.restarting && p.phase !== 'downloading' && p.phase !== 'installing'
  return (
    <div role="region" aria-label="Update" data-testid="update-notice"
      className="fixed bottom-4 left-4 z-40 w-[380px] max-w-[calc(100vw-2rem)] rounded-xl border border-line bg-panel p-4 shadow-pop animate-pop">
      {closable && <button aria-label="Dismiss" className="absolute right-2.5 top-2.5 rounded p-1 text-muted hover:bg-hover hover:text-fg" onClick={laterUpdate}><X className="size-3.5" /></button>}
      {body}
    </div>
  )
}

function Head({ icon, title, sub }: { icon: ReactNode; title: string; sub?: string }) {
  return (
    <div className="flex gap-2.5 pr-5">
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0">
        <div className="text-[13.5px] font-semibold">{title}</div>
        {sub && <div className="mt-0.5 text-[12.5px] text-muted">{sub}</div>}
      </div>
    </div>
  )
}

/** Notes shows release notes (Markdown from the commit messages) as plain blocks: headings, lists, paragraphs. */
export function Notes({ text }: { text: string }) {
  const lines = text.replace(/\r/g, '').split('\n')
  const out: ReactNode[] = []
  let list: string[] = []
  let para: string[] = []
  const flush = () => {
    if (list.length) out.push(<ul key={out.length} className="my-1 list-disc space-y-0.5 pl-4">{list.map((l, i) => <li key={i}>{inline(l)}</li>)}</ul>)
    if (para.length) out.push(<p key={out.length} className="my-1">{inline(para.join(' '))}</p>)
    list = []; para = []
  }
  let last = ''
  for (const line of lines) {
    const t = line.trim()
    if (!t) { flush(); continue }
    const h = /^#{1,6}\s+(.*)$/.exec(t)
    if (h) { flush(); out.push(<div key={out.length} className="mt-2 text-[12.5px] font-semibold first:mt-0">{inline(h[1])}</div>); continue }
    const li = /^[-*]\s+(.*)$/.exec(t)
    if (li) { if (para.length) flush(); list.push(li[1]); last = 'li'; continue }
    // A wrapped list item goes on.
    if (last === 'li' && list.length && /^\s{2,}/.test(line)) { list[list.length - 1] += ' ' + t; continue }
    if (list.length) flush()
    para.push(t)
    last = 'p'
  }
  flush()
  return <div className={clsx('text-[12px] leading-relaxed text-fg-2')}>{out}</div>
}

/** inline: `code` and **bold**; everything else as text. */
function inline(s: string): ReactNode {
  return s.split(/(`[^`]+`|\*\*[^*]+\*\*|<sub>[^<]*<\/sub>)/g).map((part, i) => {
    if (part.startsWith('`') && part.endsWith('`')) return <code key={i} className="rounded bg-hover px-1 font-mono text-[11px]">{part.slice(1, -1)}</code>
    if (part.startsWith('**') && part.endsWith('**')) return <b key={i} className="font-semibold">{part.slice(2, -2)}</b>
    if (part.startsWith('<sub>')) return <span key={i} className="font-mono text-[10.5px] text-faint">{part.slice(5, -6)}</span>
    return part
  })
}
