import { expect, test } from 'vitest'
import { statementRange } from '../components/SqlEditor'
import { cellKind } from './cellKind'
import { quote } from './quote'

test('names are quoted where Snowflake and Oracle would read them in upper case', () => {
  expect(quote('GT_A1', 'snowflake')).toBe('GT_A1')
  expect(quote('gt_a', 'snowflake')).toBe('"gt_a"')
  expect(quote('a"B', 'oracle')).toBe('"a""B"')
  expect(quote('gt_a', 'postgres')).toBe('gt_a')
  expect(quote('GT_A', 'postgres')).toBe('"GT_A"')
})

test('the statement at the cursor, with // comments and escaped quotes', () => {
  const at = (text: string, pos: number) => { const r = statementRange(text, pos, false, 'snowflake'); return r && text.slice(r[0], r[1]) }
  const s = "SELECT 'it\\'s; x' AS a;\nSELECT 2 // it's; one\n, 3;\nSELECT $$ ; $$"
  expect(at(s, 3)).toBe("SELECT 'it\\'s; x' AS a")
  expect(at(s, 30)).toBe("SELECT 2 // it's; one\n, 3")
  expect(at(s, s.length)).toBe('SELECT $$ ; $$')
})

test('cell editors by type', () => {
  expect(cellKind('TIMESTAMP_NTZ(9)', undefined, 'snowflake').kind).toBe('datetime')
  expect(cellKind('TIME(9)', undefined, 'snowflake').kind).toBe('time')
  expect(cellKind('DATE', undefined, 'snowflake').kind).toBe('date')
  expect(cellKind('BOOLEAN', undefined, 'snowflake')).toEqual({ kind: 'choice', options: [{ value: 'true', label: 'true' }, { value: 'false', label: 'false' }] })
  expect(cellKind('VARIANT', undefined, 'snowflake').kind).toBe('text')
})

test('BigQuery: triple quotes, # comments and backticks', () => {
  const at = (text: string, pos: number) => { const r = statementRange(text, pos, false, 'bigquery'); return r && text.slice(r[0], r[1]) }
  const s = "SELECT \'\'\'a;\'b;\'\'\' AS x; SELECT 2 # it's; one\n, \"c\\\";d\"; SELECT `e;f`"
  expect(at(s, 3)).toBe("SELECT \'\'\'a;\'b;\'\'\' AS x")
  expect(at(s, 34)).toBe("SELECT 2 # it's; one\n, \"c\\\";d\"")
  expect(at(s, s.length)).toBe('SELECT `e;f`')
  expect(quote('a b', 'bigquery')).toBe('`a b`')
  expect(quote('Ab', 'bigquery')).toBe('Ab')
})
