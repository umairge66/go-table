import { expect, test, type Page } from '@playwright/test'

// BigQuery through the UI. Set GOTABLE_E2E_BIGQUERY to the path of a
// service account's key; the free tier is enough.
const key = process.env.GOTABLE_E2E_BIGQUERY ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

test('BigQuery: connect, browse, estimate, filter, structure, design (needs GOTABLE_E2E_BIGQUERY)', async ({ page }) => {
  test.skip(!key, 'set GOTABLE_E2E_BIGQUERY')
  test.setTimeout(240_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  const slow = { timeout: 30_000 }

  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  await form.getByRole('radio', { name: 'BigQuery', exact: true }).click()
  await expect(form.getByLabel('Port')).toHaveCount(0)
  await expect(form.getByText('Security (SSL)')).toHaveCount(0)
  await form.getByPlaceholder('~/keys/project-key.json').fill('/nope.json')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/no such file/)).toBeVisible(slow)
  await form.getByPlaceholder('~/keys/project-key.json').fill(key)
  await form.getByLabel('Limit of a query (GB)').fill('0.1')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText('Connected successfully')).toBeVisible(slow)
  await shot(page, 'bigquery-form')
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('BigQuery', slow)

  const run = async (text: string) => {
    await page.keyboard.press('Control+t')
    const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
    await editor.click()
    await editor.fill(text)
    await page.keyboard.press('Control+Shift+Enter')
  }
  await run(`CREATE SCHEMA IF NOT EXISTS gt_ui;
CREATE OR REPLACE TABLE gt_ui.people AS SELECT n AS id, CONCAT('row ', CAST(n AS STRING)) AS name, [n, n + 1] AS tags, STRUCT(n AS a, 'x' AS b) AS st FROM UNNEST(GENERATE_ARRAY(1, 700)) AS n;
CREATE OR REPLACE VIEW gt_ui.people_view AS SELECT * FROM gt_ui.people;
SELECT 'it\\'s; done' AS msg; # a comment; here`)
  await expect(page.getByRole('gridcell', { name: "it's; done" })).toBeVisible(slow)
  await page.keyboard.press('Control+w')
  await page.reload()
  await page.goto(process.env.GOTABLE_URL!)
  await page.getByText('BigQuery').first().dblclick()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('BigQuery', slow)
  // The dataset opens (its rail tile names it).
  await page.getByRole('button', { name: 'Open database' }).click()
  await page.getByRole('dialog').getByText('gt_ui', { exact: true }).click()

  const tables = page.getByRole('navigation', { name: 'Tables' })
  await tables.getByRole('button', { name: 'people', exact: true }).click()
  const view = page.getByTestId('data-people')
  await expect(view.getByText(/1–300 of 700 rows/)).toBeVisible(slow)
  await shot(page, 'bigquery-table')
  await expect(view.getByRole('button', { name: 'Row', exact: true })).toHaveCount(0)
  await expect(view.getByRole('button', { name: 'Import' })).toBeVisible()
  await view.getByLabel('Quick search').fill('row 69')
  await page.keyboard.press('Enter')
  await expect(view.getByText(/of 11 rows/)).toBeVisible(slow)
  await tables.getByRole('button', { name: 'people_view', exact: true }).click()
  await expect(page.getByTestId('data-people_view').getByText(/of 700 rows/)).toBeVisible(slow)

  await tables.getByRole('button', { name: 'people', exact: true }).click()
  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('STRUCT<a INT64, b STRING>')).toBeVisible(slow)
  await expect(view.getByRole('button', { name: 'Add index' })).toHaveCount(0)
  await shot(page, 'bigquery-structure')

  // What a query would read shows before it runs.
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill('SELECT name FROM people')
  await expect(page.getByTestId('reads').last()).toContainText(/Reads \d/, slow)
  await page.keyboard.press('Control+Enter')
  await expect(page.getByTitle(/Results are read-only here/)).toBeVisible(slow)
  await shot(page, 'bigquery-query')
  await page.keyboard.press('Control+w')

  // Deleting.
  await tables.getByRole('button', { name: 'people_view', exact: true }).click()
  await page.keyboard.press('Delete')
  const del = page.getByRole('dialog', { name: /Delete/ })
  await expect(del.getByTestId('drop-sql')).toContainText('DROP VIEW `gt_ui`.`people_view`', slow)
  await del.getByRole('button', { name: /^Delete/ }).click()
  await expect(tables.getByRole('button', { name: 'people_view', exact: true })).toHaveCount(0, slow)

  await run('DROP SCHEMA gt_ui CASCADE; SELECT 7 AS gone;')
  await expect(page.getByRole('gridcell', { name: '7', exact: true })).toBeVisible(slow)
  expect(errors).toEqual([])
})
