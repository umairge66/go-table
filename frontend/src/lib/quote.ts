import { family, type Driver } from '../types'

/** quote: a name as SQL takes it, in quotes only where it needs them. */
export function quote(name: string, driver: Driver) {
  // Unquoted names are read in upper case there.
  if (family(driver) === 'snowflake' || family(driver) === 'oracle') return /^[A-Z][A-Z0-9_]*$/.test(name) ? name : '"' + name.replace(/"/g, '""') + '"'
  if (/^[a-z_][a-z0-9_]*$/.test(name)) return name
  if (family(driver) === 'bigquery') return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) ? name : '`' + name.replace(/[`\\]/g, '\\$&') + '`'
  switch (family(driver)) {
    case 'mysql': return '`' + name.replace(/`/g, '``') + '`'
    case 'clickhouse': return '`' + name.replace(/[`\\]/g, '\\$&') + '`'
    case 'sqlserver': return '[' + name.replace(/]/g, ']]') + ']'
  }
  return '"' + name.replace(/"/g, '""') + '"'
}
