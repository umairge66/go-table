import { describe, expect, it } from 'vitest'
import { JSONError, compactJSON, conversions, formatJSON, getAt, parseJSON, renameAt, typeOf, updateAt } from './json'

const round = (s: string) => compactJSON(parseJSON(s))

describe('lossless JSON', () => {
  it('keeps numbers, order and every key as written', () => {
    for (const s of ['{"b":9007199254740993,"a":1.0,"10":1,"2":2}', '[1e400,-0,0.10,12345678901234567890.123456789]', '{"__proto__":1,"":null,"x\\ny":"\\u00e9"}'.replace('\\u00e9', 'é'), '"a"', 'null', '[]', '{}']) {
      expect(round(s)).toBe(s)
    }
  })
  it('says what is wrong and where', () => {
    const err = (s: string) => { try { parseJSON(s) } catch (e) { return (e as JSONError).message } return '' }
    expect(err('{"a": 1,}')).toContain('a comma too many')
    expect(err('{"a" 1}')).toContain('a : goes here')
    expect(err('{\n  "a": tru\n}')).toContain('Line 2, column 8')
    expect(err("{'a': 1}")).toContain('field name in "quotes"')
    expect(err('{"a": 1, "a": 2}')).toContain('twice')
    expect(err('{"a": 1')).toContain('closing }')
    expect(err('[1 2]')).toContain('a comma or ]')
    expect(err('{"a": 01}')).toContain('isn’t JSON')
    expect(err('1 2')).toContain('after the end')
    expect(err('"abc')).toContain('no closing "')
  })
  it('formats short things on one line', () => {
    expect(formatJSON(parseJSON('{"_id":{"$oid":"507f1f77bcf86cd799439011"},"tags":["a","b"],"o":{"x":1}}')))
      .toBe('{\n  "_id": { "$oid": "507f1f77bcf86cd799439011" },\n  "tags": ["a", "b"],\n  "o": {\n    "x": 1\n  }\n}')
  })
  it('edits by path without touching the rest', () => {
    const n = parseJSON('{"a":{"b":[1,{"c":2}]},"d":9007199254740993}')
    expect(compactJSON(updateAt(n, ['a', 'b', 1, 'c'], () => ({ t: 'string', v: 'x' })))).toBe('{"a":{"b":[1,{"c":"x"}]},"d":9007199254740993}')
    expect(compactJSON(updateAt(n, ['a', 'b', 0], () => undefined))).toBe('{"a":{"b":[{"c":2}]},"d":9007199254740993}')
    expect(compactJSON(renameAt(n, ['a', 'b'], 'z'))).toBe('{"a":{"z":[1,{"c":2}]},"d":9007199254740993}')
    expect(getAt(n, ['a', 'b', 1])).toEqual(parseJSON('{"c":2}'))
  })
  it('knows the types written as objects', () => {
    const t = (s: string) => typeOf(parseJSON(s))
    expect(t('{"$oid":"507f1f77bcf86cd799439011"}')).toMatchObject({ label: 'ObjectId', text: '507f1f77bcf86cd799439011' })
    expect(() => t('{"$oid":"507f1f77bcf86cd799439011"}').read!('zz')).toThrow('24 hexadecimal')
    expect(compactJSON(t('{"$date":"2026-01-01T00:00:00Z"}').read!('2026-02-03 04:05'))).toBe('{"$date":"2026-02-03T04:05:00Z"}')
    expect(() => t('{"$date":"2026-01-01T00:00:00Z"}').read!('soon')).toThrow('written like')
    expect(t('{"$binary":{"base64":"oO68mZwLTvi7bWu5vTgKEQ==","subType":"04"}}')).toMatchObject({ label: 'UUID', text: 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11' })
    expect(t('{"$ss":["a","b"]}').items).toHaveLength(2)
    expect(t('{"$oid":1,"x":2}').label).toBe('Object')
    expect(() => t('5').read!('five')).toThrow('isn’t a number')
    expect(compactJSON(conversions('mongo').find((c) => c.label === 'Long')!.make('12'))).toBe('{"$numberLong":"12"}')
  })
})
