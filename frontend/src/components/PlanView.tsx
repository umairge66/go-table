import clsx from 'clsx'
import { AlertTriangle, ArrowUpDown, CheckCircle2, Info, Layers, ScanSearch } from 'lucide-react'
import type { PlanStage, QueryPlan } from '../types'
import { fmt } from '../views/DataView'

/**
 * PlanView draws a MongoDB explain like Compass's: the totals and what they
 * mean, then the stages from the one that returns documents down to the scans
 * that feed it.
 */
export function PlanView({ plan }: { plan: QueryPlan }) {
  const ran = plan.docsExamined !== undefined
  const returned = plan.nReturned
  // Compass's rule of thumb: examining many more documents than are returned
  // means an index would help.
  const ratio = ran && returned !== undefined ? (plan.docsExamined ?? 0) / Math.max(returned, 1) : 0
  return (
    <div className="h-full overflow-auto p-4" data-testid="plan">
      <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Stat label="Documents returned" value={returned} />
        <Stat label="Documents examined" value={plan.docsExamined} warn={ratio > 10} />
        <Stat label="Index keys examined" value={plan.keysExamined} />
        <Stat label="Execution time" value={plan.ms} unit="ms" />
      </div>
      <div className="mb-4 flex flex-wrap gap-2 text-[12.5px]" data-testid="plan-notes">
        {plan.indexes.length > 0
          ? <Note tone="ok" icon={<CheckCircle2 />}>Used {plan.indexes.length === 1 ? 'index' : 'indexes'} {plan.indexes.map((x) => <code key={x} className="font-mono">{x}</code>).reduce<React.ReactNode[]>((a, x, i) => (i ? [...a, ', ', x] : [x]), [])}</Note>
          : null}
        {plan.collScan && <Note tone="warn" icon={<AlertTriangle />}>Collection scan: every document is read. An index on the filtered fields would avoid it.</Note>}
        {plan.inMemorySort && <Note tone="warn" icon={<ArrowUpDown />}>Sorted in memory: no index gives this order.</Note>}
        {ratio > 10 && !plan.collScan && <Note tone="warn" icon={<AlertTriangle />}>{fmt(Math.round(ratio))} documents examined per document returned.</Note>}
        {plan.rejectedPlans > 0 && <Note tone="info" icon={<Layers />}>{plan.rejectedPlans} other plan{plan.rejectedPlans === 1 ? ' was' : 's were'} considered and rejected.</Note>}
        {!ran && <Note tone="info" icon={<Info />}>Planned only ({plan.verbosity}): run explain(&quot;executionStats&quot;) for counts.</Note>}
      </div>
      <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted">Stages <span className="font-normal normal-case text-faint">— the top one returns the documents</span></h3>
      <Stage s={plan.root} />
    </div>
  )
}

function Stat({ label, value, unit, warn }: { label: string; value?: number; unit?: string; warn?: boolean }) {
  return (
    <div className={clsx('rounded-xl border px-3 py-2.5', warn ? 'border-warn/40 bg-warn/10' : 'border-line bg-panel')}>
      <div className="text-[11.5px] text-muted">{label}</div>
      <div className="mt-0.5 text-[18px] font-semibold tabular-nums">{value === undefined ? <span className="text-faint">—</span> : <>{fmt(value)}{unit && <span className="ml-1 text-[12px] font-normal text-muted">{unit}</span>}</>}</div>
    </div>
  )
}

function Note({ tone, icon, children }: { tone: 'ok' | 'warn' | 'info'; icon: React.ReactElement; children: React.ReactNode }) {
  return (
    <div className={clsx('flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 [&>svg]:size-3.5 [&>svg]:shrink-0',
      tone === 'ok' ? 'border-ok/30 bg-ok/10 text-ok' : tone === 'warn' ? 'border-warn/30 bg-warn/10 text-warn' : 'border-line bg-panel-2 text-fg-2')}>
      {icon}<span className={tone === 'info' ? '' : 'text-fg'}>{children}</span>
    </div>
  )
}

const tone = (stage: string) =>
  stage === 'COLLSCAN' || stage === 'SORT' || stage === '$sort' ? 'warn'
    : /SCAN|IDHACK|EXPRESS/.test(stage) ? 'ok' : 'plain'

function Stage({ s }: { s: PlanStage }) {
  const t = tone(s.stage)
  const stats: [string, number | undefined][] = [['returned', s.nReturned], ['docs examined', s.docsExamined], ['keys examined', s.keysExamined], ['ms', s.ms]]
  return (
    <div className="relative">
      <div data-testid={`stage-${s.stage}`} className={clsx('inline-block min-w-[260px] max-w-full rounded-lg border px-3 py-2',
        t === 'warn' ? 'border-warn/40 bg-warn/5' : t === 'ok' ? 'border-ok/40 bg-ok/5' : 'border-line bg-panel')}>
        <div className="flex items-center gap-2">
          {s.stage === 'SHARD' ? <Layers className="size-3.5 text-muted" /> : t === 'ok' ? <ScanSearch className="size-3.5 text-ok" /> : t === 'warn' ? <AlertTriangle className="size-3.5 text-warn" /> : null}
          <span className="font-mono text-[12.5px] font-semibold">{s.stage}</span>
          {s.shard && <span className="font-mono text-[12px] text-muted">{s.shard}</span>}
          {s.index && <span className="rounded bg-accent-soft px-1.5 py-0.5 font-mono text-[11px] text-accent">{s.index}</span>}
          {s.direction && s.direction !== 'forward' && <span className="text-[11px] text-muted">{s.direction}</span>}
        </div>
        {(s.keyPattern || s.filter) && (
          <div className="mt-1 space-y-0.5 font-mono text-[11.5px] text-fg-2">
            {s.keyPattern && <div className="truncate" title={s.keyPattern}><span className="text-muted">keys </span>{s.keyPattern}</div>}
            {s.filter && <div className="truncate" title={s.filter}><span className="text-muted">{s.stage.startsWith('$') ? '' : 'filter '}</span>{s.filter}</div>}
          </div>
        )}
        {stats.some(([, v]) => v !== undefined) && (
          <div className="mt-1 flex flex-wrap gap-x-3 text-[11.5px] text-muted">
            {stats.filter(([, v]) => v !== undefined).map(([k, v]) => <span key={k}><b className="font-semibold tabular-nums text-fg-2">{fmt(v!)}</b> {k}</span>)}
          </div>
        )}
      </div>
      {s.children && s.children.length > 0 && (
        <div className="ml-4 border-l-2 border-line pl-4 pt-2">
          <div className={clsx(s.children.length > 1 && 'flex flex-wrap gap-4')}>
            {s.children.map((c, i) => <div key={i} className="pt-1"><Stage s={c} /></div>)}
          </div>
        </div>
      )}
    </div>
  )
}
