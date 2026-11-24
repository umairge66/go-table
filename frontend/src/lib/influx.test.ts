import { expect, test } from 'vitest'
import { statementRange } from '../components/SqlEditor'
import { influxStatementRange, writeBlocks } from './influx'

const s = `SELECT 1;
SELECT 2;

WRITE
cpu,host=it's\\ "5" v=1,s="a;

b" 1
cpu v=2 2

SELECT 3`
const at = (needle: string) => {
  const r = influxStatementRange(s, s.indexOf(needle), (t, p) => statementRange(t, p))
  return r && s.slice(r[0], r[1])
}

test('a WRITE block runs as a whole, SQL around it by itself', () => {
  expect(writeBlocks(s)).toHaveLength(1)
  for (const n of ['WRITE', 'cpu,host', 'b" 1', 'cpu v=2']) expect(at(n)).toBe('WRITE\ncpu,host=it\'s\\ "5" v=1,s="a;\n\nb" 1\ncpu v=2 2')
  expect(at('1;')).toBe('SELECT 1')
  expect(at('2;')).toBe('SELECT 2')
  expect(at('3')).toBe('SELECT 3')
})
