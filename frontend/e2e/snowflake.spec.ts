import { expect, test, type Page } from '@playwright/test'

// Snowflake through the UI. Set GOTABLE_E2E_SNOWFLAKE to JSON with account,
// user, key (the private key file), warehouse, role and database.
const env = process.env.GOTABLE_E2E_SNOWFLAKE ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

test('Snowflake: sign in, browse, edit, count, structure, delete (needs GOTABLE_E2E_SNOWFLAKE)', async ({ page }) => {
  test.skip(!env, 'set GOTABLE_E2E_SNOWFLAKE')
  test.setTimeout(240_000)
  const a = JSON.parse(env) as Record<string, string>
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  const slow = { timeout: 30_000 }

  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.keyboard.press('Control+n')
  await form.getByRole('radio', { name: 'Snowflake', exact: true }).click()
  // No host, port, SSL or SSH.
  await expect(form.getByLabel('Port')).toHaveCount(0)
  await expect(form.getByText('Security (SSL)')).toHaveCount(0)
  await expect(form.getByText('Connect through an SSH tunnel')).toHaveCount(0)
  await form.getByLabel('Account').fill(a.account)
  await form.getByLabel('User', { exact: true }).fill(a.user)
  await form.getByLabel('Warehouse').fill(a.warehouse)
  await form.getByLabel('Role').fill(a.role)
  await form.getByLabel('Database', { exact: true }).fill(a.database)
  await form.getByLabel('Password').fill('wrong')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText('Wrong user name or password', { exact: true })).toBeVisible(slow)
  await form.getByLabel('Sign in with').selectOption('browser')
  await expect(form.getByLabel('Password')).toHaveCount(0)
  await form.getByLabel('Sign in with').selectOption('key')
  await form.getByPlaceholder('~/.ssh/snowflake_key.p8').fill(a.key)
  await form.getByLabel('Key passphrase').fill('')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/Snowflake \d/)).toBeVisible(slow)
  await shot(page, 'snowflake-form')
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('Snowflake', slow)

  const run = async (text: string) => {
    await page.keyboard.press('Control+t')
    const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
    await editor.click()
    await editor.fill(text)
    await page.keyboard.press('Control+Shift+Enter')
  }
  await run(`DROP TABLE IF EXISTS GT_UI CASCADE; DROP TABLE IF EXISTS GT_UI_KID; DROP VIEW IF EXISTS GT_UI_VIEW;
CREATE TABLE GT_UI (ID INT PRIMARY KEY, NAME VARCHAR(40) NOT NULL, SEEN TIMESTAMP_NTZ, OK BOOLEAN, DOC VARIANT); // a comment; with a semicolon
INSERT INTO GT_UI SELECT SEQ4() + 1, 'row ' || (SEQ4() + 1), '2024-03-15 10:20:30', TRUE, PARSE_JSON('{"a": 1}') FROM TABLE(GENERATOR(ROWCOUNT => 700));
CREATE TABLE GT_UI_KID (ID INT PRIMARY KEY, P INT, CONSTRAINT GT_UI_FK FOREIGN KEY (P) REFERENCES GT_UI (ID));
CREATE VIEW GT_UI_VIEW AS SELECT * FROM GT_UI;
SELECT 'it\\'s; done' AS MSG, 1.5::NUMBER(10,2) AS N;`)
  await expect(page.getByRole('gridcell', { name: "it's; done" })).toBeVisible(slow)
  await shot(page, 'snowflake-query')
  await page.keyboard.press('Control+w')

  const tables = page.getByRole('navigation', { name: 'Tables' })
  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await tables.getByRole('button', { name: 'GT_UI', exact: true }).click()
  const view = page.getByTestId('data-GT_UI')
  // A table's own count is free.
  await expect(view.getByText(/of 700 rows/)).toBeVisible(slow)
  await shot(page, 'snowflake-table')
  await view.getByRole('gridcell', { name: 'row 1', exact: true }).first().dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type("it's \\ 1")
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText(`UPDATE "PUBLIC"."GT_UI" SET "NAME" = 'it''s \\\\ 1' WHERE "ID" = '1'`)).toBeVisible(slow)
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Saved 1 change' })).toBeVisible(slow)

  // A view isn't counted until asked.
  await tables.getByRole('button', { name: 'GT_UI_VIEW', exact: true }).click()
  const v = page.getByTestId('data-GT_UI_VIEW')
  await expect(v.getByText(/of more than 300 rows/)).toBeVisible(slow)
  await shot(page, 'snowflake-more')
  await v.getByRole('button', { name: 'Next page' }).click()
  await expect(v.getByText(/301–600 of more than 600 rows/)).toBeVisible(slow)
  await v.getByRole('button', { name: 'Next page' }).click()
  await expect(v.getByText(/601–700 of 700 rows/)).toBeVisible(slow)
  await v.getByRole('button', { name: 'Previous page' }).click()
  await v.getByRole('button', { name: 'Count' }).click()
  await expect(v.getByText(/301–600 of 700 rows/)).toBeVisible(slow)

  // Structure: keys that aren't enforced, no plain indexes.
  await tables.getByRole('button', { name: 'GT_UI', exact: true }).click()
  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('NUMBER(38,0)').first()).toBeVisible(slow)
  await expect(view.getByText('not enforced').first()).toBeVisible()
  await shot(page, 'snowflake-structure')
  await view.getByRole('tab', { name: 'Definition' }).click()
  await expect(view.getByText(/CREATE TABLE GT_UI/)).toBeVisible(slow)

  // The parent can't go while a key points at it.
  await tables.getByRole('button', { name: 'GT_UI', exact: true }).click()
  await page.keyboard.press('Delete')
  const del = page.getByRole('dialog', { name: /Delete/ })
  await expect(del.getByTestId('drop-sql')).toContainText('DROP TABLE "PUBLIC"."GT_UI" RESTRICT', slow)
  await shot(page, 'snowflake-delete')
  await del.getByRole('button', { name: /^Delete/ }).click()
  await expect(del.getByText(/another table has a foreign key/)).toBeVisible(slow)
  await page.keyboard.press('Escape')

  // A new table, made in the designer.
  await run('DROP VIEW GT_UI_VIEW; DROP TABLE GT_UI_KID; DROP TABLE GT_UI; SELECT 7 AS GONE;')
  await expect(page.getByRole('gridcell', { name: '7', exact: true })).toBeVisible(slow)
  expect(errors).toEqual([])
})
