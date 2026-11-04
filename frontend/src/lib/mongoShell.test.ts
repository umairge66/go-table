import { expect, test } from 'vitest'
import { formatMongo, mongoColl, mongoSourceTable, mongoStatementAt, mongoStatements } from './mongoShell'

test('statements end at a semicolon or a line end outside brackets', () => {
  const text = `db.a.find({ x: "a;b",
  y: /;\\/"/ })
  .limit(1)

db.b.drop(); db.c.drop()
// only a comment
db.d.find()`
  const parts = mongoStatements(text).map(([a, b]) => text.slice(a, b).trim())
  expect(parts[0]).toBe(`db.a.find({ x: "a;b",\n  y: /;\\/"/ })\n  .limit(1)`)
  expect(parts.slice(1, 3)).toEqual(['db.b.drop()', 'db.c.drop()'])
})

test('the statement under the cursor', () => {
  const text = 'db.a.find()\n\ndb.b.find({\n  x: 1\n})\n.limit(2)\n'
  expect(mongoStatementAt(text, 3)).toBe('db.a.find()')
  expect(mongoStatementAt(text, text.indexOf('x: 1'))).toBe('db.b.find({\n  x: 1\n})\n.limit(2)')
  expect(mongoStatementAt('// nothing here', 3)).toBe('')
})

test('only a plain find of whole documents can be edited', () => {
  expect(mongoSourceTable('db.people.find()')).toEqual({ schema: '', name: 'people' })
  expect(mongoSourceTable('db.people.find({ a: ")" }).sort({ a: 1 }).limit(5);')).toEqual({ schema: '', name: 'people' })
  expect(mongoSourceTable('db.getCollection("odd-name").find({})')).toEqual({ schema: '', name: 'odd-name' })
  expect(mongoSourceTable('db.people.find({}, { name: 1 })')).toBeNull()
  expect(mongoSourceTable('db.people.find({}).project({ name: 1 })')).toBeNull()
  expect(mongoSourceTable('db.people.aggregate([])')).toBeNull()
  expect(mongoSourceTable('db.people.findOne()')).toBeNull()
  expect(mongoSourceTable('db.people.find({ a: [1, 2] })')).toEqual({ schema: '', name: 'people' })
})

test('collections with unusual names', () => {
  expect(mongoColl('people')).toBe('db.people')
  expect(mongoColl('odd-name')).toBe('db.getCollection("odd-name")')
})

test('formatting keeps short commands on a line and breaks long ones', () => {
  expect(formatMongo('db.a.find( {x:1,   y:"a  b"} ).limit(5)')).toBe('db.a.find({ x: 1, y: "a  b" }).limit(5);')
  expect(formatMongo('db.a.find({ d: new Date("2024"), r: /a b/i })')).toBe('db.a.find({ d: new Date("2024"), r: /a b/i });')
  const long = formatMongo('db.people.aggregate([{$match:{age:{$gt:30},city:"Berlin",name:{$regex:"^a"}}},{$group:{_id:"$city",total:{$sum:1},oldest:{$max:"$age"}}}])')
  expect(long.split('\n').length).toBeGreaterThan(2)
  expect(formatMongo(long)).toBe(long) // stable
  expect(long.replace(/\s/g, '')).toBe('db.people.aggregate([{$match:{age:{$gt:30},city:"Berlin",name:{$regex:"^a"}}},{$group:{_id:"$city",total:{$sum:1},oldest:{$max:"$age"}}}]);'.replace(/\s/g, ''))
  expect(formatMongo('// keep me\ndb.a.find( {x:1} )')).toContain('// keep me')
  expect(() => formatMongo('db.a.find({ x: 1')).toThrow()
})
