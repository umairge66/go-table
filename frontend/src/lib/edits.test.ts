import { addRow, cellAt, emptyEdits, pendingCount, rowStatus, setCell, toChanges, toggleDelete } from './edits'

const rows = [['1', 'Alice', null], ['2', 'Bob', 'Paris'], ['3', 'Carol', 'Rome']]
const cols = ['id', 'name', 'city']

test('edit, revert, delete, insert and convert to changes', () => {
  let e = emptyEdits()
  e = setCell(rows, e, 0, 2, 'Berlin')
  e = setCell(rows, e, 1, 1, 'Robert')
  e = setCell(rows, e, 1, 1, 'Bob') // back to the original value
  expect(cellAt(rows, e, 0, 2)).toEqual({ value: 'Berlin', status: 'edited' })
  expect(cellAt(rows, e, 1, 1).status).toBe('clean')
  expect(rowStatus(rows, e, 1)).toBe('clean')

  e = toggleDelete(rows, e, [2])
  expect(rowStatus(rows, e, 2)).toBe('deleted')
  expect(setCell(rows, e, 2, 1, 'x')).toBe(e) // deleted rows are read-only

  e = addRow(e, 3)
  e = setCell(rows, e, 3, 1, 'Dave')
  expect(cellAt(rows, e, 3, 2)).toEqual({ value: null, status: 'inserted' })
  expect(pendingCount(e)).toBe(3)

  expect(toChanges(rows, e, cols, [0])).toEqual([
    { kind: 'update', key: { id: '1' }, values: { city: 'Berlin' } },
    { kind: 'delete', key: { id: '3' } },
    { kind: 'insert', values: { name: 'Dave' } },
  ])
})

test('rows without a primary key are matched on every column', () => {
  const e = setCell(rows, emptyEdits(), 0, 1, 'Al')
  expect(toChanges(rows, e, cols, [])[0].key).toEqual({ id: '1', name: 'Alice', city: null })
})

test('deleting an edited row drops the edit; deleting a new row removes it', () => {
  let e = setCell(rows, emptyEdits(), 0, 1, 'x')
  e = toggleDelete(rows, e, [0])
  expect(e.updates.size).toBe(0)
  e = toggleDelete(rows, e, [0])
  expect(e.deleted.size).toBe(0)
  e = addRow(addRow(e, 3), 3)
  e = toggleDelete(rows, e, [3])
  expect(e.inserted.length).toBe(1)
})
