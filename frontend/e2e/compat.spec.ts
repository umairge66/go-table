import { expect, test } from '@playwright/test'

// PostgreSQL- and MySQL-compatible servers, driven through the UI. Set
// GOTABLE_E2E_COMPAT to space-separated URLs, e.g.
//   cockroachdb://root@127.0.0.1:26257/defaultdb?sslmode=disable
//   yugabytedb://yugabyte:yugabyte@127.0.0.1:5433/yugabyte?sslmode=disable
//   tidb://root@127.0.0.1:4000/test
//   sqlserver://sa:pw@127.0.0.1:1433?database=gotable
//   clickhouse://gotable:pw@127.0.0.1:9000/gotable
//   duckdb:///tmp/compat.duckdb
// scripts/test-dbs.sh with KEEP=1 prints the ports of its containers.
const urls = (process.env.GOTABLE_E2E_COMPAT ?? '').split(/\s+/).filter(Boolean)
const LABELS: Record<string, string> = { cockroachdb: 'CockroachDB', yugabytedb: 'YugabyteDB', tidb: 'TiDB', timescaledb: 'TimescaleDB',
  percona: 'Percona Server', singlestore: 'SingleStore', sqlserver: 'SQL Server', clickhouse: 'ClickHouse', duckdb: 'DuckDB' }

/** The test table in each database's own SQL. */
const DDL: Record<string, string> = {
  tidb: 'CREATE TABLE gt_compat (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, seen DATETIME)',
  percona: 'CREATE TABLE gt_compat (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, seen DATETIME)',
  singlestore: 'CREATE TABLE gt_compat (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, seen DATETIME)',
  sqlserver: 'CREATE TABLE gt_compat (id INT PRIMARY KEY, name NVARCHAR(40) NOT NULL, seen DATETIME2)',
  clickhouse: 'CREATE TABLE gt_compat (id UInt32, name String, seen Nullable(DateTime)) ENGINE = MergeTree ORDER BY id',
}

test('compatible servers: connect, browse, edit and commit (needs GOTABLE_E2E_COMPAT)', async ({ browser }) => {
  test.skip(!urls.length, 'set GOTABLE_E2E_COMPAT to cockroachdb://, yugabytedb:// or tidb:// URLs')
  test.setTimeout(urls.length * 90_000)
  for (const url of urls) {
    const scheme = url.slice(0, url.indexOf(':'))
    // Plain postgres:// and mysql:// URLs of cloud services are told apart by host.
    const label = /\.neon\.tech[:/]/.test(url) ? 'Neon' : /\.supabase\.(co|com)[:/]/.test(url) ? 'Supabase' : /\.psdb\.cloud[:/]/.test(url) ? 'PlanetScale' : LABELS[scheme]
    await test.step(label ?? url, async () => {
      const page = await browser.newPage()
      const errors: string[] = []
      page.on('pageerror', (e) => errors.push(e.message))
      await page.goto(process.env.GOTABLE_URL!)
      const form = page.getByLabel('Connection form')
      await page.keyboard.press('Control+n')
      await form.getByRole('button', { name: 'Paste URL' }).click()
      await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
      await page.getByRole('button', { name: 'Fill in' }).click()
      await expect(form.getByRole('radio', { name: label })).toHaveAttribute('aria-checked', 'true')
      await form.getByRole('button', { name: 'Save & connect' }).click()
      await expect(page.getByTestId('server-info').locator('visible=true')).toContainText(label)

      const runSQL = async (sql: string) => {
        await page.keyboard.press('Control+t')
        const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
        await editor.click()
        await editor.fill(sql)
        await page.keyboard.press('Control+Shift+Enter')
      }
      await runSQL(`DROP TABLE IF EXISTS gt_compat;
${DDL[scheme] ?? 'CREATE TABLE gt_compat (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, seen TIMESTAMP)'};
INSERT INTO gt_compat VALUES (1, 'one', '2024-03-15 10:20:30'), (2, 'two', NULL);`)
      // Three statements ran (ClickHouse reports no row count for the INSERT).
      await expect.poll(() => page.getByText(/OK — \d+ row\(s\) affected/).count()).toBe(3)
      await page.keyboard.press('Control+w')
      await page.getByRole('button', { name: 'Refresh tables' }).click()
      await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'gt_compat', exact: true }).click()

      const view = page.getByTestId('data-gt_compat')
      const cells = view.getByRole('gridcell')
      await expect(cells.nth(1)).toHaveText('one')
      await cells.nth(1).dblclick()
      await page.keyboard.type('!')
      await page.keyboard.press('Enter')
      // The timestamp column gets the calendar.
      await cells.nth(5).dblclick()
      await expect(page.getByTestId('cell-picker')).toBeVisible()
      await page.keyboard.press('Escape')
      await page.keyboard.press('Control+s')
      await page.getByRole('dialog', { name: 'Review changes' }).getByRole('button', { name: 'Commit' }).click()
      await expect(page.getByRole('status').filter({ hasText: 'Saved 1 change' })).toBeVisible()
      // Without an ORDER BY the edited row may come back in another place.
      await expect(view.getByRole('gridcell', { name: 'one!', exact: true })).toBeVisible()

      await view.getByRole('tab', { name: 'Structure' }).click()
      await expect(view.getByText('seen', { exact: true })).toBeVisible()
      await view.getByRole('tab', { name: 'Data' }).click()
      await runSQL('DROP TABLE gt_compat')
      expect(errors).toEqual([])
      await page.close()
    })
  }
})
