import { expect, test } from 'vitest'
import { statementAt, statementRange } from './SqlEditor'
import { mongoStatementRange } from '../lib/mongoShell'

const text = `SELECT
  *
FROM
  users;

SELECT id
FROM orders -- the ; in a comment; doesn't end it
WHERE note = 'a;b';
`
const at = (needle: string, d = 0) => text.indexOf(needle) + d
const lines = (r: [number, number] | null) => r && [text.slice(0, r[0]).split('\n').length, text.slice(0, r[1]).split('\n').length]

test('Run takes the statement under the cursor, as before (no closing ;)', () => {
  expect(statementAt(text, at('*'))).toBe('SELECT\n  *\nFROM\n  users')
  expect(statementAt(text, at('orders'))).toBe("SELECT id\nFROM orders -- the ; in a comment; doesn't end it\nWHERE note = 'a;b'")
})

test('the highlight covers every line of that statement, with its ;', () => {
  // Anywhere in the first statement: lines 1 to 4.
  expect(lines(statementRange(text, at('SELECT'), true))).toEqual([1, 4])
  expect(lines(statementRange(text, at('users;', 6), true))).toEqual([1, 4]) // right after the ;
  expect(text.slice(...statementRange(text, 0, true)!)).toBe('SELECT\n  *\nFROM\n  users;')
  // The blank line just below it is no longer it: only its own lines are.
  expect(statementRange(text, at('\n\nSELECT id', 1), true)).toBeNull()
  expect(lines(statementRange(text, at('WHERE'), true))).toEqual([6, 8])
})

test('typing a new statement below moves the highlight to it', () => {
  const more = 'SELECT 1;\n\nSELECT 2'
  expect(statementAt(more, more.length)).toBe('SELECT 2')
  expect(statementRange('SELECT 1;\n', 'SELECT 1;\n'.length, true)).toBeNull() // Enter after it: the cursor has left it
  expect(statementRange('', 0, true)).toBeNull()
})

test('a cursor away from every statement picks none', () => {
  // As in the screenshot: a blank line with blank lines around it, between two statements.
  const two = 'SELECT *\nFROM users;\n\n\n\nSELECT *\nFROM colors;'
  const blank = two.indexOf('\n\n\n') + 2 // the middle blank line
  expect(statementRange(two, blank, true)).toBeNull()
  expect(statementAt(two, blank)).toBe('')
  expect(statementRange('SELECT 1;\n\n', 'SELECT 1;\n\n'.length, true)).toBeNull() // two lines below: none
  // On the line above the second statement it is still none: the cursor isn't at it yet.
  expect(statementRange(two, two.indexOf('SELECT *\nFROM colors') - 1, true)).toBeNull()
  // Two statements on one line: the one at the cursor.
  const one = 'SELECT 1; SELECT 2;'
  expect(statementAt(one, 3)).toBe('SELECT 1')
  expect(statementAt(one, one.length - 2)).toBe('SELECT 2')
  expect(statementAt(one, 9)).toBe('SELECT 1') // right after its ;
})

test('MongoDB commands: one a line, or spread over lines', () => {
  const m = 'db.a.find({\n  x: 1\n})\n.limit(2)\n\ndb.b.drop()'
  expect(mongoStatementRange(m, 3)).toEqual([0, m.indexOf('\n\n')])
  expect(m.slice(...mongoStatementRange(m, m.length)!)).toBe('db.b.drop()')
  // One a line: the start of a line is that line's command, not the one above.
  const lines2 = 'db.a.find()\ndb.b.find()'
  expect(lines2.slice(...mongoStatementRange(lines2, lines2.indexOf('db.b'))!)).toBe('db.b.find()')
  expect(mongoStatementRange('db.a.find()\n\n\n\ndb.b.find()', 13)).toBeNull()
})
