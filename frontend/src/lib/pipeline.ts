// The aggregation pipeline builder's logic: stages as text, put together
// into the shell commands the server runs (see views/PipelineView.tsx).
import { mongoColl } from './mongoShell'

export interface Stage { id: string; op: string; body: string; enabled: boolean }

/** STAGES: the operators offered, each with a template body. */
export const STAGES: [string, string][] = [
  ['$match', '{\n  field: "value"\n}'],
  ['$project', '{\n  _id: 0,\n  field: 1\n}'],
  ['$group', '{\n  _id: "$field",\n  count: { $sum: 1 }\n}'],
  ['$sort', '{\n  field: -1\n}'],
  ['$limit', '10'],
  ['$skip', '10'],
  ['$unwind', '"$field"'],
  ['$lookup', '{\n  from: "other",\n  localField: "field",\n  foreignField: "_id",\n  as: "joined"\n}'],
  ['$addFields', '{\n  newField: "$field"\n}'],
  ['$set', '{\n  field: "value"\n}'],
  ['$unset', '"field"'],
  ['$count', '"count"'],
  ['$sortByCount', '"$field"'],
  ['$replaceRoot', '{\n  newRoot: "$field"\n}'],
  ['$facet', '{\n  byField: [{ $sortByCount: "$field" }]\n}'],
  ['$bucket', '{\n  groupBy: "$field",\n  boundaries: [0, 10, 100],\n  default: "other"\n}'],
  ['$sample', '{\n  size: 10\n}'],
  ['$out', '"collection"'],
  ['$merge', '{\n  into: "collection"\n}'],
]

/** WRITES: stages that write documents somewhere, so they never run as a preview. */
export const WRITES = ['$out', '$merge']

export const template = (op: string) => STAGES.find(([o]) => o === op)?.[1] ?? '{}'

/** stageText writes one stage: { $match: { … } }. */
export const stageText = (s: Stage) => `{ ${s.op}: ${s.body.trim() || '{}'} }`

/** pipelineText writes the enabled stages as an array, one a line. */
export function pipelineText(stages: Stage[]): string {
  const on = stages.filter((s) => s.enabled)
  if (!on.length) return '[]'
  return `[\n${on.map((s) => '  ' + stageText(s).replace(/\n/g, '\n  ')).join(',\n')}\n]`
}

/** aggregateCommand is the whole pipeline as a command for the query editor. */
export const aggregateCommand = (collection: string, stages: Stage[]) => `${mongoColl(collection)}.aggregate(${pipelineText(stages)})`

/**
 * previewCommand runs the enabled stages up to and including stages[upTo],
 * limited to n documents; null when that would write ($out, $merge) or the
 * stage is off. Each document comes back whole in one field, doc, so a field
 * that is null is told apart from one that isn't there.
 */
export function previewCommand(collection: string, stages: Stage[], upTo: number, n = 10): string | null {
  const s = stages[upTo]
  if (!s?.enabled) return null
  const before = stages.slice(0, upTo + 1).filter((x) => x.enabled)
  if (before.some((x) => WRITES.includes(x.op))) return null
  return `${mongoColl(collection)}.aggregate(${pipelineText([...before, { id: '', op: '$limit', body: String(n), enabled: true }, { id: '', op: '$replaceRoot', body: '{ newRoot: { doc: "$$ROOT" } }', enabled: true }])})`
}
