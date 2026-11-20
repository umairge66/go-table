import clsx from 'clsx'
import { RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { api, type FieldReport, type SchemaReport, type TableRef } from '../api'
import { Button, Select, Spinner, errorParts } from '../components/ui'
import type { SessionInfo } from '../types'
import { fmt } from './DataView'

const SAMPLES = [100, 1000, 10000]

// One color per kind of BSON type, mixed from the theme's tokens so both
// themes work: text, numbers, dates, ids, booleans, documents, arrays.
const mix = (a: string, b: string) => `color-mix(in srgb, var(--${a}) 55%, var(--${b}))`
const TYPE_COLORS: Record<string, string> = {
  string: 'var(--accent)', int: 'var(--ok)', long: 'var(--ok)', double: 'var(--ok)', decimal: 'var(--ok)',
  date: 'var(--warn)', objectId: 'var(--fg-2)', bool: mix('accent', 'err'), object: mix('ok', 'accent'), array: mix('warn', 'err'),
  null: 'var(--faint)',
}
const typeColor = (t: string) => TYPE_COLORS[t] ?? 'var(--muted)'
const pct = (n: number, of: number) => (of ? Math.round((n / of) * 100) : 0)

/**
 * SchemaView is Compass's Schema tab: from a random sample of documents,
 * each field (nested ones and array elements too) with its types, how often
 * it is there, its range and its most common values.
 */
export function SchemaView({ session, table }: { session: SessionInfo; table: TableRef }) {
  const [sample, setSample] = useState(1000)
  const [report, setReport] = useState<SchemaReport | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const analyze = useCallback(async () => {
    setBusy(true)
    setError('')
    try {
      setReport(await api.schemaAnalysis(session.id, table, sample))
    } catch (e) {
      setError(errorParts(e).filter(Boolean).join(' — '))
    } finally {
      setBusy(false)
    }
  }, [session.id, table, sample])
  useEffect(() => { analyze() }, [analyze])

  return (
    <div className="flex h-full flex-col" data-testid="schema">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3 text-[12.5px]">
        <span className="text-muted" data-testid="schema-sampled">
          {report ? <>Sampled <b className="font-semibold text-fg">{fmt(report.sampled)}</b> of about {fmt(report.total)} documents, at random</> : 'Analyzing…'}
        </span>
        <div className="flex-1" />
        <span className="text-muted">Documents to sample</span>
        <Select className="w-28" aria-label="Sample size" value={sample} onChange={(e) => setSample(Number(e.target.value))}>
          {SAMPLES.map((n) => <option key={n} value={n}>{fmt(n)}</option>)}
        </Select>
        <Button size="sm" variant="ghost" icon={busy ? <Spinner className="size-3.5" /> : <RefreshCw className="size-3.5" />} onClick={analyze} disabled={busy}>Analyze again</Button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-4">
        {error && <div className="rounded-lg border border-err/30 bg-err/5 p-3 text-[12.5px] text-err">{error}</div>}
        {report && !report.fields.length && !error && <div className="text-[12.5px] text-muted">No documents to analyze.</div>}
        {report && report.fields.length > 0 && (
          <div className="overflow-hidden rounded-xl border border-line">
            {report.fields.map((f) => <FieldRow key={f.path} f={f} />)}
          </div>
        )}
      </div>
    </div>
  )
}

/** scope: what a field's share is out of, as [one, many]: documents, array elements, or documents where its parent is one. */
function scope(path: string): [string, string] {
  if (path.endsWith('[]')) return ['element', 'elements']
  const dot = path.lastIndexOf('.')
  if (dot < 0) return ['document', 'documents']
  const parent = path.slice(0, dot)
  return parent.endsWith('[]')
    ? [`${parent.slice(0, -2)} element that is a document`, `${parent.slice(0, -2)} elements that are documents`]
    : [`document where ${parent} is a document`, `documents where ${parent} is a document`]
}

function FieldRow({ f }: { f: FieldReport }) {
  // a.b[].c: the depth from its separators, the name from its last part.
  const depth = (f.path.match(/\.|\[\]/g) ?? []).length
  const name = f.path.endsWith('[]') ? '[ elements ]' : f.path.slice(f.path.lastIndexOf('.') + 1)
  const presence = pct(f.count, f.of)
  const numeric = f.types.some((t) => ['int', 'long', 'double', 'decimal', 'date'].includes(t.type))
  const topTotal = f.top?.reduce((a, v) => a + v.count, 0) ?? 0
  return (
    <div className="grid grid-cols-[minmax(180px,1.1fr)_minmax(200px,1.4fr)_minmax(220px,1.6fr)] gap-4 border-b border-line-2 px-3 py-2.5 last:border-0" data-testid={`field-${f.path}`}>
      <div className="min-w-0" style={{ paddingLeft: depth * 16 }}>
        <div className={clsx('truncate font-mono text-[12.5px]', f.path.endsWith('[]') ? 'text-muted' : 'font-semibold')} title={f.path}>{name}</div>
        <div className="text-[11.5px] text-muted">
          {presence === 100 ? `in every ${scope(f.path)[0]}` : `in ${presence}% of ${scope(f.path)[1]}`}
          {presence < 100 && <span className="text-faint"> ({fmt(f.count)} of {fmt(f.of)})</span>}
        </div>
      </div>
      <div className="min-w-0">
        <div className="flex h-2.5 overflow-hidden rounded-full bg-hover" aria-label="Types">
          {f.types.map((t) => <div key={t.type} style={{ width: `${pct(t.count, f.count)}%`, background: typeColor(t.type) }} title={`${t.type}: ${pct(t.count, f.count)}%`} />)}
        </div>
        <div className="mt-1 flex flex-wrap gap-x-3 text-[11.5px]" data-testid="field-types">
          {f.types.map((t) => (
            <span key={t.type} className="inline-flex items-center gap-1">
              <span className="size-2 rounded-full" style={{ background: typeColor(t.type) }} />
              <span className="font-mono">{t.type}</span>
              {f.types.length > 1 && <span className="text-muted">{pct(t.count, f.count)}%</span>}
            </span>
          ))}
        </div>
      </div>
      <div className="min-w-0 text-[11.5px]">
        {numeric && f.min !== undefined && (
          <div className="font-mono text-fg-2" data-testid="field-range"><span className="text-muted">from </span>{f.min}<span className="text-muted"> to </span>{f.max}</div>
        )}
        {f.unique && <div className="text-muted">Every value differs{f.distinct ? ` (${fmt(f.distinct)})` : ''}.</div>}
        {f.top && f.top.length > 0 && (
          <div className="mt-0.5 space-y-0.5" data-testid="field-top">
            {f.top.slice(0, 5).map((v) => (
              <div key={v.value} className="flex items-center gap-2">
                <div className="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-hover"><div className="h-full bg-accent" style={{ width: `${pct(v.count, topTotal)}%` }} /></div>
                <span className="min-w-0 truncate font-mono" title={v.value}>{v.value || <span className="text-faint">(empty)</span>}</span>
                <span className="shrink-0 text-muted">{fmt(v.count)}</span>
              </div>
            ))}
            {f.distinct > 5 && <div className="text-faint">{f.distinct >= 1000 ? 'over 1,000' : fmt(f.distinct)} different values</div>}
          </div>
        )}
      </div>
    </div>
  )
}
