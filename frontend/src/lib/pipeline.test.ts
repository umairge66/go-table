import { expect, test } from 'vitest'
import { aggregateCommand, pipelineText, previewCommand, type Stage } from './pipeline'

const st = (op: string, body: string, enabled = true): Stage => ({ id: op, op, body, enabled })

test('a pipeline is the enabled stages, one a line', () => {
  const stages = [st('$match', '{ a: 1 }'), st('$sort', '{\n  b: -1\n}', false), st('$limit', '5')]
  expect(pipelineText(stages)).toBe('[\n  { $match: { a: 1 } },\n  { $limit: 5 }\n]')
  expect(pipelineText([])).toBe('[]')
  expect(aggregateCommand('odd-name', [st('$count', '"n"')])).toBe('db.getCollection("odd-name").aggregate([\n  { $count: "n" }\n])')
  // A body left empty is an empty document, not a syntax error.
  expect(pipelineText([st('$match', '  ')])).toBe('[\n  { $match: {} }\n]')
})

test('a preview runs the stages up to one, limited', () => {
  const stages = [st('$match', '{ a: 1 }'), st('$group', '{ _id: "$b" }', false), st('$sort', '{ _id: 1 }')]
  const whole = '  { $replaceRoot: { newRoot: { doc: "$$ROOT" } } }\n'
  expect(previewCommand('people', stages, 0)).toBe('db.people.aggregate([\n  { $match: { a: 1 } },\n  { $limit: 10 },\n' + whole + '])')
  expect(previewCommand('people', stages, 2, 3)).toBe('db.people.aggregate([\n  { $match: { a: 1 } },\n  { $sort: { _id: 1 } },\n  { $limit: 3 },\n' + whole + '])')
  // A stage that's off has no preview of its own.
  expect(previewCommand('people', stages, 1)).toBeNull()
  // Nothing that writes runs as a preview, nor anything after it.
  const out = [st('$match', '{}'), st('$out', '"copy"'), st('$limit', '1')]
  expect(previewCommand('people', out, 0)).not.toBeNull()
  expect(previewCommand('people', out, 1)).toBeNull()
  expect(previewCommand('people', out, 2)).toBeNull()
})
