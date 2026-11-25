import { expect, test, type Page } from '@playwright/test'

// DynamoDB through the UI. Set GOTABLE_E2E_DYNAMODB to a DynamoDB Local
// URL, e.g. dynamodb://127.0.0.1:8000/us-east-1
const url = process.env.GOTABLE_E2E_DYNAMODB ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

const runner = (page: Page) => async (text: string) => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(text)
  await page.keyboard.press('Control+Shift+Enter')
}

test('DynamoDB: connect, PartiQL, browse, edit (needs GOTABLE_E2E_DYNAMODB)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_DYNAMODB')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  await form.getByRole('radio', { name: 'DynamoDB', exact: true }).click()
  await expect(form.getByLabel('Endpoint host (DynamoDB Local)')).toHaveValue('')
  await expect(form.getByLabel('Access key ID')).toBeVisible()
  await expect(form.getByLabel('Region')).toBeVisible()
  await shot(page, 'dynamodb-form')
  // Another server gets its usual host back.
  await form.getByRole('radio', { name: 'PostgreSQL', exact: true }).click()
  await expect(form.getByLabel('Host', { exact: true })).toHaveValue('127.0.0.1')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/DynamoDB \(local endpoint\)/)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('DynamoDB')

  const run = runner(page)
  await run(`DROP TABLE IF EXISTS "gt_e2e";
CREATE TABLE "gt_e2e" ("id" N PARTITION KEY);
INSERT INTO "gt_e2e" VALUE {'id': 1, 'name': 'Alice', 'tags': <<'a', 'b'>>};
INSERT INTO "gt_e2e" VALUE {'id': 2, 'name': 'Bob'};
SELECT * FROM "gt_e2e" WHERE "id" = 1;`)
  await expect(page.getByRole('gridcell', { name: '{"$ss":["a","b"]}', exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')

  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'gt_e2e', exact: true }).click()
  const view = page.getByTestId('data-gt_e2e')
  const cell = (name: string) => view.getByRole('gridcell', { name, exact: true })
  await expect(cell('Bob')).toBeVisible()
  await shot(page, 'dynamodb-table')
  await cell('Bob').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type("Bob's")
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText(/UPDATE "gt_e2e" SET "name" = "Bob's" WHERE "id" = 2/)).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(review).toBeHidden()
  await expect(cell("Bob's")).toBeVisible()

  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('partition key')).toBeVisible()
  await expect(view.getByRole('button', { name: 'Add column' })).toHaveCount(0)
  await shot(page, 'dynamodb-structure')
  await view.getByRole('tab', { name: 'Definition' }).click()
  await expect(view.getByText(/KeySchema/).first()).toBeVisible()
  await page.keyboard.press('Control+w')

  // New table opens a template rather than the designer.
  await page.getByRole('button', { name: /^New table/ }).first().click()
  await expect(page.getByTestId('sql-editor').last()).toContainText('PARTITION KEY')
  await run('DROP TABLE "gt_e2e"')
  expect(errors).toEqual([])
})
