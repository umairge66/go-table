import { expect, test } from '@playwright/test'

// PostgreSQL- and MySQL-compatible servers, driven through the UI. Set
// GOTABLE_E2E_COMPAT to space-separated URLs, e.g.
//   cockroachdb://root@127.0.0.1:26257/defaultdb?sslmode=disable
//   yugabytedb://yugabyte:yugabyte@127.0.0.1:5433/yugabyte?sslmode=disable
//   tidb://root@127.0.0.1:4000/test
// scripts/test-dbs.sh with KEEP=1 prints the ports of its containers.
const urls = (process.env.GOTABLE_E2E_COMPAT ?? '').split(/\s+/).filter(Boolean)
const LABELS: Record<string, string> = { cockroachdb: 'CockroachDB', yugabytedb: 'YugabyteDB', tidb: 'TiDB' }

test('compatible servers: connect, browse, edit and commit (needs GOTABLE_E2E_COMPAT)', async ({ browser }) => {
  test.skip(!urls.length, 'set GOTABLE_E2E_COMPAT to cockroachdb://, yugabytedb:// or tidb:// URLs')
  test.setTimeout(urls.length * 90_000)
  for (const url of urls) {
    const label = LABELS[url.slice(0, url.indexOf(':'))]
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
      const mysql = label === 'TiDB'
      await runSQL(`DROP TABLE IF EXISTS gt_compat;
CREATE TABLE gt_compat (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, seen ${mysql ? 'DATETIME' : 'TIMESTAMP'});
INSERT INTO gt_compat VALUES (1, 'one', '2024-03-15 10:20:30'), (2, 'two', NULL);`)
      await expect(page.getByText('2 row(s) affected').last()).toBeVisible()
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
      await expect(cells.nth(1)).toHaveText('one!')

      await view.getByRole('tab', { name: 'Structure' }).click()
      await expect(view.getByText('seen', { exact: true })).toBeVisible()
      await view.getByRole('tab', { name: 'Data' }).click()
      await runSQL('DROP TABLE gt_compat')
      expect(errors).toEqual([])
      await page.close()
    })
  }
})
