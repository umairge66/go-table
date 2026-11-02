import { format, type SqlLanguage } from 'sql-formatter'
import { family, forDriver, type Driver, type PerDriver } from '../types'

const languages: PerDriver<SqlLanguage> = {
  postgres: 'postgresql',
  mysql: 'mysql',
  mariadb: 'mariadb',
  tidb: 'tidb',
  sqlite: 'sqlite',
}

/**
 * formatSQL beautifies SQL in the connection's dialect: upper-case keywords,
 * two-space indentation, one clause per line, statements separated by a
 * blank line. Throws if the SQL can't be parsed.
 */
export function formatSQL(sql: string, driver: Driver): string {
  const out = format(sql, {
    language: forDriver(languages, driver),
    keywordCase: 'upper',
    dataTypeCase: 'upper',
    functionCase: 'lower',
    tabWidth: 2,
    linesBetweenQueries: 1,
    // PostgreSQL $1 / MySQL ? placeholders and :name parameters stay as they are.
    paramTypes: family(driver) === 'postgres' ? { positional: false, numbered: ['$'] } : { positional: true },
  })
  return sql.endsWith('\n') ? out + '\n' : out
}
