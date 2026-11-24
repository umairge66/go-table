import { expect, test, type Page } from '@playwright/test'

// InfluxDB 3 through the UI: writing, SQL, a table's points, an edit. Set
// GOTABLE_E2E_INFLUX to a URL, e.g. influxdb://127.0.0.1:8181/gotable
// (influxdb://TOKEN@host:8181/db with a token).
const url = process.env.GOTABLE_E2E_INFLUX ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

test('InfluxDB: connect, write, browse, edit (needs GOTABLE_E2E_INFLUX)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_INFLUX')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.keyboard.press('Control+n')
  await form.getByRole('radio', { name: 'InfluxDB 3', exact: true }).click()
  await expect(form.getByLabel('Port')).toHaveValue('8181')
  await expect(form.getByText('Token', { exact: true })).toBeVisible()
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/InfluxDB 3\./)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('InfluxDB')

  const run = async (text: string) => {
    await page.keyboard.press('Control+t')
    const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
    await editor.click()
    await editor.fill(text)
    await page.keyboard.press('Control+Shift+Enter')
  }
  await run(`DROP TABLE IF EXISTS gt_e2e;
CREATE TABLE gt_e2e (site TAG, temp FLOAT64, note UTF8);

WRITE
gt_e2e,site=roof temp=21.5,note="first; one" 1700000000123456789
gt_e2e,site=cellar temp=12 1700000000123456790

SELECT site, temp FROM gt_e2e WHERE site = 'roof'`)
  await expect(page.getByRole('gridcell', { name: 'roof', exact: true }).first()).toBeVisible()
  await shot(page, 'influx-editor')
  await page.keyboard.press('Control+w')

  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'gt_e2e', exact: true }).click()
  const view = page.getByTestId('data-gt_e2e')
  const cell = (name: string) => view.getByRole('gridcell', { name, exact: true })
  await expect(cell('2023-11-14T22:13:20.123456789')).toBeVisible()
  await shot(page, 'influx-table')

  await cell('first; one').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('say "hi"')
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText('WRITE gt_e2e,site=roof note="say \\"hi\\"" 1700000000123456789')).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(review).toBeHidden()
  await expect(cell('say "hi"')).toBeVisible()
  await expect(cell('21.5')).toBeVisible()

  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('site', { exact: true })).toBeVisible()
  await shot(page, 'influx-structure')
  await run('DROP TABLE gt_e2e')
  expect(errors).toEqual([])
})
