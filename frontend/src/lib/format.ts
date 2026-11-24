import { formatRedis } from './redisShell'
import { formatMongo } from './mongoShell'
import { formatElastic } from './elasticConsole'
import { format, type SqlLanguage } from 'sql-formatter'
import { family, forDriver, type Driver, type PerDriver } from '../types'

const languages: PerDriver<SqlLanguage> = {
  postgres: 'postgresql',
  mysql: 'mysql',
  mariadb: 'mariadb',
  tidb: 'tidb',
  singlestore: 'singlestoredb',
  sqlite: 'sqlite',
  sqlserver: 'transactsql',
  oracle: 'plsql',
  mongodb: 'sql', // never used: see formatSQL
  redis: 'sql', // never used: see formatSQL
  dynamodb: 'sql', // never used: see formatSQL
  elasticsearch: 'sql', // never used: see formatSQL
  influxdb: 'postgresql',
  snowflake: 'snowflake',
  bigquery: 'bigquery',
  cassandra: 'sql', // CQL reads close enough to plain SQL
  duckdb: 'duckdb',
  clickhouse: 'clickhouse',
}

/**
 * formatSQL beautifies SQL in the connection's dialect: upper-case keywords,
 * two-space indentation, one clause per line, statements separated by a
 * blank line. Throws if the SQL can't be parsed.
 */
export function formatSQL(sql: string, driver: Driver): string {
  if (family(driver) === 'mongodb') return formatMongo(sql)
  if (family(driver) === 'redis') return formatRedis(sql)
  if (family(driver) === 'elasticsearch') return formatElastic(sql)
  if (family(driver) === 'influxdb' && /^[ \t]*WRITE[ \t]*$/im.test(sql)) return sql.trim() + '\n' // line protocol is written as it must be
  if (family(driver) === 'dynamodb') return sql.trim() + '\n' // PartiQL's values and PUT's JSON aren't SQL to a formatter
  const out = format(sql, {
    language: forDriver(languages, driver),
    keywordCase: 'upper',
    dataTypeCase: 'upper',
    functionCase: 'lower',
    tabWidth: 2,
    linesBetweenQueries: 1,
    // PostgreSQL $1 / MySQL ? placeholders and :name parameters stay as they are.
    paramTypes: family(driver) === 'postgres' ? { positional: false, numbered: ['$'] }
      : family(driver) === 'duckdb' ? { positional: true, numbered: ['$'] }
        : family(driver) === 'sqlserver' ? { positional: false, named: ['@'] }
          : family(driver) === 'oracle' ? { positional: false, numbered: [':'] } : { positional: true },
  })
  return sql.endsWith('\n') ? out + '\n' : out
}
