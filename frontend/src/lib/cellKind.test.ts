import { cellKind, fmtDateTime, parseDate, parseTime } from './cellKind'

test('column types pick their editor', () => {
  expect(cellKind('date', undefined, 'mysql').kind).toBe('date')
  for (const t of ['datetime', 'DATETIME(3)', 'timestamp', 'timestamp without time zone', 'timestamp(6) with time zone', 'timestamptz'])
    expect(cellKind(t, undefined, 'postgres').kind).toBe('datetime')
  for (const t of ['time', 'time(3)', 'time without time zone', 'timetz']) expect(cellKind(t, undefined, 'postgres').kind).toBe('time')
  expect(cellKind("enum('a','b')", ['a', 'b'], 'mysql')).toEqual({ kind: 'choice', options: [{ value: 'a', label: 'a' }, { value: 'b', label: 'b' }] })
  expect(cellKind('mood', ['sad', 'ok'], 'postgres').kind).toBe('choice') // a PostgreSQL enum type
  expect(cellKind("set('x','y')", ['x', 'y'], 'mysql')).toEqual({ kind: 'set', options: ['x', 'y'] })
  expect(cellKind('boolean', undefined, 'postgres')).toMatchObject({ options: [{ value: 'true' }, { value: 'false' }] })
  expect(cellKind('tinyint(1)', undefined, 'mysql')).toMatchObject({ options: [{ value: '1' }, { value: '0' }] })
  expect(cellKind('BOOLEAN', undefined, 'sqlite')).toMatchObject({ options: [{ value: '1' }, { value: '0' }] })
  for (const t of ['varchar(255)', 'int', 'tinyint(4)', 'text', 'json']) expect(cellKind(t, undefined, 'mysql').kind).toBe('text')
})

test('dates and times round-trip in the server format', () => {
  expect(parseDate('2024-02-29')).toMatchObject({ y: 2024, m: 2, d: 29, h: 0 })
  const p = parseDate('2024-02-29 13:05:09.25+02:00')!
  expect(fmtDateTime(p)).toBe('2024-02-29 13:05:09.25+02:00')
  expect(fmtDateTime({ ...p, d: 1, frac: '' })).toBe('2024-02-01 13:05:09+02:00')
  expect(parseDate('0000-00-00')).toBeNull()
  expect(parseDate('not a date')).toBeNull()
  expect(parseTime('7:30')).toEqual({ h: 7, mi: 30, s: 0, frac: '', tz: '' })
  expect(parseTime('10:00:00+02')).toMatchObject({ h: 10, tz: '+02' })
})
