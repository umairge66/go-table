import { sourceTable } from './sourceTable'

test('plain selects from one table are editable', () => {
  expect(sourceTable('select * from users;')).toEqual({ schema: '', name: 'users' })
  expect(sourceTable('SELECT id, name FROM shop.users u WHERE id > 3 ORDER BY name LIMIT 10')).toEqual({ schema: 'shop', name: 'users' })
  expect(sourceTable('select u.* from "My Table" as u')).toEqual({ schema: '', name: 'My Table' })
  expect(sourceTable('select `id` from `db`.`t` -- comment\n')).toEqual({ schema: 'db', name: 't' })
  expect(sourceTable("select * from t where name = 'a, b (c)' and id in (select id from t2)")).toEqual({ schema: '', name: 't' })
})

test('anything else is read-only', () => {
  for (const q of [
    'select * from a join b on a.id = b.a_id',
    'select * from a, b',
    'select count(*) from a',
    'select id as x from a',
    'select distinct name from a',
    'select name from a group by name',
    'select * from a where id = 1 union select * from b',
    'select * from (select * from a) s',
    'insert into a values (1)',
    'select 1',
  ]) expect(sourceTable(q)).toBeNull()
})
