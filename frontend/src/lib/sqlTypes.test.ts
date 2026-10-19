import { describe, expect, it } from 'vitest'
import { findType, paramProblem, parseType, typeGroups, typeString } from './sqlTypes'

describe('sqlTypes', () => {
  it('builds type strings from the designer fields', () => {
    expect(typeString({ base: 'VARCHAR', param: '255', unsigned: false, array: false, custom: false })).toBe('VARCHAR(255)')
    expect(typeString({ base: 'INT', param: '', unsigned: true, array: false, custom: false })).toBe('INT UNSIGNED')
    expect(typeString({ base: 'text', param: '', unsigned: false, array: true, custom: false })).toBe('text[]')
    expect(typeString({ base: ' citext ', param: '9', unsigned: true, array: false, custom: true })).toBe('citext')
  })

  it('parses type strings back, keeping unknown ones as custom', () => {
    expect(parseType('mysql', 'decimal(10,2) unsigned')).toEqual({ base: 'DECIMAL', param: '10,2', unsigned: true, array: false, custom: false })
    expect(parseType('postgres', 'double precision')).toMatchObject({ base: 'double precision', custom: false })
    expect(parseType('postgres', 'text[]')).toMatchObject({ base: 'text', array: true })
    expect(parseType('postgres', 'mood', ['mood'])).toMatchObject({ base: 'mood', custom: false })
    expect(parseType('postgres', 'geography(Point, 4326)')).toMatchObject({ custom: true, base: 'geography(Point, 4326)' })
  })

  it('explains missing or malformed lengths', () => {
    const varchar = findType('mysql', 'varchar')
    expect(paramProblem(varchar, '')).toBe('VARCHAR needs a length')
    expect(paramProblem(varchar, 'abc')).toBe('length is a whole number')
    expect(paramProblem(varchar, '190')).toBe('')
    expect(paramProblem(findType('postgres', 'varchar'), '')).toBe('') // optional on PostgreSQL
    expect(paramProblem(findType('mysql', 'enum'), "'a', 'b'")).toBe('')
    expect(paramProblem(findType('mysql', 'enum'), 'a,b')).toBe("values look like 'a','b'")
    expect(paramProblem(findType('mysql', 'datetime'), '7')).toBe('fractional seconds is 0–6')
  })

  it('groups types like phpMyAdmin and adds the database’s own', () => {
    expect(typeGroups('mysql').map((g) => g.label)).toEqual(['Numeric', 'Date and time', 'String', 'JSON', 'Spatial'])
    expect(typeGroups('postgres', ['mood']).at(-1)).toMatchObject({ label: 'Defined in this database', types: [{ name: 'mood' }] })
  })
})
