import { formatSQL } from './format'

test('formats a query with upper-case keywords and clauses on their own lines', () => {
  expect(formatSQL("select id,name from customers where city='Berlin' and balance>100 order by name", 'postgres')).toBe(
    `SELECT
  id,
  name
FROM
  customers
WHERE
  city = 'Berlin'
  AND balance > 100
ORDER BY
  name`)
})

test('keeps several statements apart and preserves strings and comments', () => {
  const out = formatSQL("-- keep me\nupdate t set a='x;y' where id=1;delete from t where id=2", 'sqlite')
  expect(out).toContain('-- keep me')
  expect(out).toContain("a = 'x;y'")
  expect(out.split(';').length).toBe(3)
  expect(out).toMatch(/;\n\nDELETE FROM/)
})

test('uses the dialect: MySQL backticks and PostgreSQL dollar quoting', () => {
  expect(formatSQL('select `order` from `my table`', 'mysql')).toBe('SELECT\n  `order`\nFROM\n  `my table`')
  const fn = formatSQL('create function f() returns int as $$ select 1 $$ language sql', 'postgres')
  expect(fn).toContain('$$ select 1 $$')
})

test('data types are upper-cased and functions lower-cased', () => {
  expect(formatSQL('CREATE TABLE t (id int, n varchar(10)); SELECT COUNT(*) FROM t', 'postgres')).toContain('id INT')
  expect(formatSQL('SELECT COUNT(*) FROM t', 'postgres')).toContain('count(*)')
})

test('invalid SQL throws instead of producing garbage', () => {
  expect(() => formatSQL("select 'unterminated", 'postgres')).toThrow()
})

test('compatible servers format like the engine they follow', () => {
  expect(formatSQL('select `order` from `t`', 'tidb')).toBe('SELECT\n  `order`\nFROM\n  `t`')
  expect(formatSQL('select $1::int', 'cockroachdb')).toBe('SELECT\n  $1::INT')
  expect(formatSQL('select $1::int', 'yugabytedb')).toBe('SELECT\n  $1::INT')
})

test('shortcut hints read the Mac way on a Mac', async () => {
  const { keys, macKeys } = await import('./platform')
  expect(keys('Ctrl+Shift+I', false)).toBe('Ctrl+Shift+I')
  expect(macKeys('Ctrl+Shift+I')).toBe('⌘⇧I')
  expect(macKeys('Ctrl S')).toBe('⌘S')
  expect(macKeys('Ctrl ↵')).toBe('⌘↵')
  expect(macKeys('Row inspector (Ctrl+Shift+I)')).toBe('Row inspector (⌘⇧I)')
  expect(macKeys('Ctrl+Tab')).toBe('⌃Tab')
  expect(macKeys('Alt+Up')).toBe('⌥Up')
})
