import { expect, test } from 'vitest'
import { elasticStatements, formatElastic, indentJSON } from './elasticConsole'

test('a script is cut up as the server side does', () => {
  const s = `# c
GET /_cat/indices?v

PUT /shop
{
  "a": { "b": 1 }
}
// another
POST shop/_bulk
{"index":{"_id":"1"}}
{"q":"GET /not/a/request; } \\" {"}

POST /shop/_search
{ "x": """
GET /inside
""" }
SELECT a,
  b FROM shop;
-- note
SELECT 2;
FROM shop
| LIMIT 5;
DELETE /shop`
  const got = elasticStatements(s).map(([a, b]) => s.slice(a, b))
  expect(got.map((x) => x.split('\n')[0])).toEqual(['GET /_cat/indices?v', 'PUT /shop', 'POST shop/_bulk', 'POST /shop/_search', 'SELECT a,', 'SELECT 2;', 'FROM shop', 'DELETE /shop'])
  expect(got[2].split('\n')).toHaveLength(3)
  expect(got[3]).toContain('GET /inside')
})

test('formatting keeps digits, comments and bulk bodies', () => {
  expect(indentJSON('{"a":9007199254740993,"b":[],"c":{"d":"x, {y}: \\" z"}}')).toBe('{\n  "a": 9007199254740993,\n  "b": [],\n  "c": {\n    "d": "x, {y}: \\" z"\n  }\n}')
  expect(formatElastic('# hi\nPOST /a/_bulk\n{"index":{}}\n{"x":1}\n\nGET /a/_search {"size":1}\n')).toBe('# hi\nPOST /a/_bulk\n{"index":{}}\n{"x":1}\n\nGET /a/_search\n{\n  "size": 1\n}\n')
  expect(() => formatElastic('GET /a/_search\n{"size":')).toThrow()
})
