import { KeyRound, Plus } from 'lucide-react'
import { Button } from '../components/ui'
import type { Column, Index } from '../types'

export function StructureView({ columns, indexes, onAddColumn, highlight }: {
  columns: Column[]
  indexes: Index[]
  /** onAddColumn opens the add column dialog; omitted for views. */
  onAddColumn?: () => void
  /** highlight marks a just-added column. */
  highlight?: string
}) {
  return (
    <div className="h-full overflow-auto p-4">
      <div className="mb-2 flex items-center">
        <h3 className="text-[12px] font-semibold uppercase tracking-wider text-muted">Columns <span className="font-normal text-faint">{columns.length}</span></h3>
        <div className="flex-1" />
        {onAddColumn && <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={onAddColumn}>Add column</Button>}
      </div>
      <div className="overflow-hidden rounded-xl border border-line">
        <table className="w-full text-[12.5px]">
          <thead className="bg-panel-2 text-left text-[11.5px] text-muted">
            <tr>{['#', 'Name', 'Type', 'Nullable', 'Default', 'Comment'].map((h) => <th key={h} className="border-b border-line px-3 py-2 font-semibold">{h}</th>)}</tr>
          </thead>
          <tbody>
            {columns.map((c, i) => (
              <tr key={c.name} className={`border-b border-line-2 last:border-0 hover:bg-hover ${c.name === highlight ? 'bg-inserted animate-fade' : ''}`}>
                <td className="w-10 px-3 py-1.5 text-faint">{i + 1}</td>
                <td className="px-3 py-1.5 font-medium"><span className="inline-flex items-center gap-1.5">{c.primaryKey && <KeyRound className="size-3 text-warn" />}{c.name}</span></td>
                <td className="px-3 py-1.5 font-mono text-[12px] text-accent">{c.type}</td>
                <td className="px-3 py-1.5">{c.nullable ? <span className="text-muted">yes</span> : <span className="font-medium">NOT NULL</span>}</td>
                <td className="px-3 py-1.5 font-mono text-[12px] text-muted">{c.default ?? <span className="text-faint">—</span>}</td>
                <td className="px-3 py-1.5 text-muted">{c.comment}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <h3 className="mb-2 mt-6 text-[12px] font-semibold uppercase tracking-wider text-muted">Indexes <span className="font-normal text-faint">{indexes.length}</span></h3>
      {indexes.length === 0 ? <div className="text-[12.5px] text-muted">No indexes.</div> : (
        <div className="overflow-hidden rounded-xl border border-line">
          <table className="w-full text-[12.5px]">
            <thead className="bg-panel-2 text-left text-[11.5px] text-muted">
              <tr>{['Name', 'Columns', 'Kind'].map((h) => <th key={h} className="border-b border-line px-3 py-2 font-semibold">{h}</th>)}</tr>
            </thead>
            <tbody>
              {indexes.map((x) => (
                <tr key={x.name} className="border-b border-line-2 last:border-0 hover:bg-hover">
                  <td className="px-3 py-1.5 font-medium">{x.name}</td>
                  <td className="px-3 py-1.5 font-mono text-[12px]">{x.columns.join(', ')}</td>
                  <td className="px-3 py-1.5">
                    {x.primary ? <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-semibold text-warn">PRIMARY</span>
                      : x.unique ? <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[11px] font-semibold text-accent">UNIQUE</span>
                        : <span className="text-muted">index</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
