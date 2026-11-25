import { features, isBigQuery, isD1, isDynamo, isSnowflake, type Connection } from '../types'

/** subtitle says where a connection goes, in one line. */
export function subtitle(c: Connection) {
  if (features(c.driver).file) return c.path ?? ''
  if (isD1(c.driver)) return `Cloudflare · ${c.database ?? ''}`
  if (isBigQuery(c.driver)) return `Google Cloud · ${c.database || 'the credentials’ project'}`
  if (isSnowflake(c.driver)) return `${c.user ? c.user + '@' : ''}${c.host ?? ''}${c.database ? '/' + c.database : ''}`
  if (isDynamo(c.driver) && !c.host) return `AWS ${c.database ?? ''}`.trim()
  let s = `${c.host || '127.0.0.1'}${c.port ? ':' + c.port : ''}`
  if (c.user) s = `${c.user}@${s}`
  if (c.database) s += `/${c.database}`
  return s
}
