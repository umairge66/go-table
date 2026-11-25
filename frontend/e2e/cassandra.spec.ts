import { expect, test, type Page } from '@playwright/test'

// Cassandra (or ScyllaDB) through the UI: CQL in the editor, a table's
// rows, an edit guarded by IF EXISTS. Set GOTABLE_E2E_CASSANDRA to a URL
// whose keyspace exists, e.g. cassandra://127.0.0.1:9042/gotable
const url = process.env.GOTABLE_E2E_CASSANDRA ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

const runner = (page: Page) => async (text: string) => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(text)
  await page.keyboard.press('Control+Shift+Enter')
}

test('Cassandra: connect, CQL, browse, edit (needs GOTABLE_E2E_CASSANDRA)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_CASSANDRA')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  await form.getByRole('radio', { name: 'Cassandra', exact: true }).click()
  await expect(form.getByLabel('Port')).toHaveValue('9042')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/(Cassandra|ScyllaDB) \d/)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText(/Cassandra|ScyllaDB/)

  const run = runner(page)
  await run(`DROP TABLE IF EXISTS gt_e2e;
CREATE TABLE gt_e2e (id int PRIMARY KEY, name text, tags set<text>);
INSERT INTO gt_e2e (id, name, tags) VALUES (1, 'Alice', {'a', 'b'});
INSERT INTO gt_e2e (id, name) VALUES (2, 'Bob');
SELECT * FROM gt_e2e WHERE id = 1;`)
  await expect(page.getByRole('gridcell', { name: "{'a', 'b'}", exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')

  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'gt_e2e', exact: true }).click()
  const view = page.getByTestId('data-gt_e2e')
  const cell = (name: string) => view.getByRole('gridcell', { name, exact: true })
  await expect(cell('Bob')).toBeVisible()
  await shot(page, 'cassandra-table')

  await cell('Bob').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type("Bob's")
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText(/UPDATE .*"name" = 'Bob''s' WHERE "id" = 2 IF EXISTS/)).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(review).toBeHidden()
  await expect(cell("Bob's")).toBeVisible()

  // The structure shows the key's role.
  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('partition key')).toBeVisible()
  await shot(page, 'cassandra-structure')

  await run('DROP TABLE gt_e2e')
  expect(errors).toEqual([])
})
