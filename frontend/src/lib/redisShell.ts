// The query editor on Redis: commands as redis-cli takes them, one a line
// (internal/db/redis_cmd.go reads them the same way).

import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import { StreamLanguage } from '@codemirror/language'
import { pickStatement, trimRange } from './mongoShell'

/** redisLines: the range of each command line (comments and blank lines left out). */
export function redisLines(text: string): [number, number][] {
  const out: [number, number][] = []
  let pos = 0
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (t && !t.startsWith('#') && !t.startsWith('//')) out.push(trimRange(text, [pos, pos + line.length]))
    pos += line.length + 1
  }
  return out
}

/** redisStatementRange: the command on the cursor's line, as Run takes it. */
export const redisStatementRange = (text: string, pos: number) => pickStatement(text, redisLines(text), pos)

export function redisStatementAt(text: string, pos: number): string {
  const r = redisStatementRange(text, pos)
  return r ? text.slice(r[0], r[1]) : ''
}

/** formatRedis tidies a script: one command a line, without stray spaces. */
export const formatRedis = (text: string) => text.split('\n').map((l) => l.trim()).join('\n').replace(/\n{3,}/g, '\n\n').trim() + '\n'

/** redisLanguage colours a command line: its name, strings, numbers and comments. */
export const redisLanguage = StreamLanguage.define<{ first: boolean }>({
  name: 'redis',
  startState: () => ({ first: true }),
  token(stream, state) {
    if (stream.sol()) state.first = true
    if (stream.eatSpace()) return null
    if (stream.sol() && (stream.match('#') || stream.match('//'))) { stream.skipToEnd(); return 'comment' }
    const q = stream.peek()
    if (q === '"' || q === "'") {
      stream.next()
      let escaped = false
      for (let c = stream.next(); c != null; c = stream.next()) {
        if (c === q && !escaped) break
        escaped = !escaped && c === '\\'
      }
      state.first = false
      return 'string'
    }
    stream.eatWhile(/\S/)
    const word = stream.current()
    if (state.first) { state.first = false; return 'keyword' }
    return /^-?\d+(\.\d+)?$/.test(word) ? 'number' : null
  },
  languageData: { commentTokens: { line: '#' } },
})

/** COMMANDS offered by autocomplete: the common ones, by family. */
export const REDIS_COMMANDS = [
  'GET', 'SET', 'GETSET', 'GETDEL', 'GETEX', 'MGET', 'MSET', 'SETNX', 'SETEX', 'APPEND', 'STRLEN', 'GETRANGE', 'SETRANGE', 'INCR', 'INCRBY', 'INCRBYFLOAT', 'DECR', 'DECRBY',
  'DEL', 'UNLINK', 'EXISTS', 'TYPE', 'RENAME', 'RENAMENX', 'COPY', 'EXPIRE', 'PEXPIRE', 'EXPIREAT', 'PEXPIREAT', 'PERSIST', 'TTL', 'PTTL', 'EXPIRETIME', 'KEYS', 'SCAN', 'RANDOMKEY', 'DUMP', 'RESTORE', 'OBJECT ENCODING', 'MEMORY USAGE',
  'HGET', 'HSET', 'HSETNX', 'HMGET', 'HGETALL', 'HDEL', 'HEXISTS', 'HKEYS', 'HVALS', 'HLEN', 'HINCRBY', 'HINCRBYFLOAT', 'HSCAN', 'HRANDFIELD',
  'LPUSH', 'RPUSH', 'LPOP', 'RPOP', 'LRANGE', 'LINDEX', 'LSET', 'LLEN', 'LREM', 'LTRIM', 'LINSERT', 'LMOVE', 'BLPOP', 'BRPOP',
  'SADD', 'SREM', 'SMEMBERS', 'SISMEMBER', 'SMISMEMBER', 'SCARD', 'SINTER', 'SUNION', 'SDIFF', 'SPOP', 'SRANDMEMBER', 'SSCAN', 'SMOVE',
  'ZADD', 'ZREM', 'ZRANGE', 'ZRANGEBYSCORE', 'ZREVRANGE', 'ZSCORE', 'ZRANK', 'ZREVRANK', 'ZCARD', 'ZCOUNT', 'ZINCRBY', 'ZPOPMIN', 'ZPOPMAX', 'ZSCAN',
  'XADD', 'XRANGE', 'XREVRANGE', 'XLEN', 'XDEL', 'XTRIM', 'XREAD', 'XINFO STREAM', 'XGROUP CREATE', 'XREADGROUP', 'XACK',
  'PFADD', 'PFCOUNT', 'GEOADD', 'GEOSEARCH', 'GEODIST', 'SETBIT', 'GETBIT', 'BITCOUNT',
  'MULTI', 'EXEC', 'DISCARD', 'WATCH', 'UNWATCH', 'PUBLISH', 'EVAL', 'EVALSHA', 'FUNCTION LIST', 'FCALL',
  'SELECT', 'DBSIZE', 'FLUSHDB', 'INFO', 'PING', 'ECHO', 'TIME', 'CONFIG GET', 'CONFIG SET', 'CLIENT LIST', 'SLOWLOG GET', 'MEMORY STATS', 'LATENCY LATEST', 'ACL WHOAMI', 'ACL LIST',
  'JSON.GET', 'JSON.SET', 'JSON.DEL',
]

/** redisCompletions completes the command at the start of a line. */
export function redisCompletions(ctx: CompletionContext): CompletionResult | null {
  const line = ctx.state.doc.lineAt(ctx.pos)
  const before = line.text.slice(0, ctx.pos - line.from)
  const m = /^\s*([A-Za-z.]*(?: [A-Za-z]*)?)$/.exec(before)
  if (!m || (!m[1] && !ctx.explicit)) return null
  return {
    from: ctx.pos - m[1].length,
    options: REDIS_COMMANDS.map((c) => ({ label: c, type: 'keyword' })),
    validFor: /^[A-Za-z. ]*$/,
  }
}

// A "table" on Redis is a SCAN MATCH pattern (internal/db/redis_cmd.go):
// user:* lists keys; a pattern without wildcards is one key.

/** isPattern: the name lists keys (it has an unescaped *, ? or [). */
export function isPattern(name: string): boolean {
  for (let i = 0; i < name.length; i++) {
    if (name[i] === '\\') { i++; continue }
    if ('*?['.includes(name[i])) return true
  }
  return false
}

/** escapeKey writes a key as the pattern that matches only it. */
export const escapeKey = (key: string) => key.replace(/[*?[\]\\]/g, (c) => '\\' + c)

/** unescapeKey reads a key back from such a pattern. */
export const unescapeKey = (pattern: string) => pattern.replace(/\\(.)/g, '$1')

/** quoteArg writes an argument as redis-cli reads it. */
export const quoteArg = (s: string) => (/^[^\s"'\\\x00-\x1f\x7f]+$/.test(s) ? s : JSON.stringify(s))

/** keyLabel: how the sidebar names a pattern or key. */
export const keyLabel = (name: string) => (name === '*' ? 'All keys' : isPattern(name) ? name : unescapeKey(name))
