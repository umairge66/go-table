import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'

// A database in the rail can be copied by name and deleted: the command is
// shown first and the name must be typed. DuckDB (schemas) runs everywhere;
// a PostgreSQL server (databases and schemas) when GOTABLE_E2E_PG is set.

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

async function newPage(browser: import('@playwright/test').Browser) {
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await context.newPage()
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)
  return { page, errors }
}

async function runSQL(page: Page, sql: string) {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(sql)
  await page.keyboard.press('Control+Shift+Enter')
  await expect(page.getByTestId('messages').getByText(/OK|row\(s\)/).last()).toBeVisible()
  await page.keyboard.press('Control+w')
}

async function openFromPicker(page: Page, name: string) {
  await page.keyboard.press('Control+d')
  const picker = page.getByRole('dialog', { name: 'Open database' })
  await picker.getByLabel('Search commands').fill(name)
  await picker.getByRole('option', { name: new RegExp(`^${name}`) }).first().click()
}

const clipboard = (page: Page) => page.evaluate(() => navigator.clipboard.readText())

test('DuckDB: copy a schema’s name, delete it (with what is in it)', async ({ browser }) => {
  const { page, errors } = await newPage(browser)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'DuckDB' }).click()
  await form.getByRole('textbox', { name: /Database file/ }).fill(join(process.env.GOTABLE_TMP!, 'drop.duckdb'))
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByText('Jump anywhere')).toBeVisible()
  await runSQL(page, 'CREATE SCHEMA stage; CREATE TABLE stage.t (x int)')

  const rail = page.getByRole('navigation', { name: 'Open databases' })
  await openFromPicker(page, 'stage')
  const tile = rail.getByRole('button', { name: /^Database stage/ })
  await expect(tile).toHaveAttribute('aria-pressed', 'true')

  // Copy name.
  await tile.click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Copy name' }).click()
  await expect.poll(() => clipboard(page)).toBe('stage')

  // Delete: the command, CASCADE for a schema that isn't empty, the name typed.
  await tile.click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Delete schema stage…' }).click()
  const dlg = page.getByRole('dialog', { name: 'Delete schema' })
  await expect(dlg.getByTestId('drop-preview')).toHaveText('DROP SCHEMA "stage" RESTRICT;')
  await dlg.getByRole('switch', { name: /Also delete everything in it/ }).click()
  await expect(dlg.getByTestId('drop-preview')).toHaveText('DROP SCHEMA "stage" CASCADE;')
  const del = dlg.getByRole('button', { name: 'Delete schema' })
  await expect(del).toBeDisabled()
  await dlg.getByLabel('Type the name to confirm').fill('stag')
  await expect(del).toBeDisabled()
  await dlg.getByLabel('Type the name to confirm').fill('stage')
  await shot(page, 'drop-schema')
  await del.click()
  await expect(dlg).toBeHidden()
  await expect(tile).toHaveCount(0)
  await page.keyboard.press('Control+d')
  const picker = page.getByRole('dialog', { name: 'Open database' })
  await expect(picker.getByRole('option', { name: /^stage/ })).toHaveCount(0)
  // The picker offers it too, choosing among what can go; main can't.
  await picker.getByRole('option', { name: /Delete a schema/ }).click()
  const choose = page.getByRole('dialog', { name: 'Delete schema' })
  await expect(choose.getByLabel('Database to delete')).toContainText('main')
  await expect(choose.getByRole('alert')).toContainText('belongs to the server')
  await choose.getByRole('button', { name: 'Cancel' }).click()
  await page.close()
  expect(errors).toEqual([])
})

test('SQLite: copy the name, and no delete (a database is a file)', async ({ browser }) => {
  const { page } = await newPage(browser)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(join(process.env.GOTABLE_TMP!, 'drop.sqlite'))
  await form.getByRole('button', { name: 'Save & connect' }).click()
  const tile = page.getByRole('navigation', { name: 'Open databases' }).getByRole('button', { name: /^Database main/ })
  await tile.click({ button: 'right' })
  await expect(page.getByRole('menuitem', { name: 'Copy name' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /^Delete/ })).toHaveCount(0)
  await page.close()
})

test('PostgreSQL: delete a database (open in the rail) and a schema (needs GOTABLE_E2E_PG)', async ({ browser }) => {
  const url = process.env.GOTABLE_E2E_PG ?? ''
  test.skip(!url, 'set GOTABLE_E2E_PG')
  const { page, errors } = await newPage(browser)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('PostgreSQL')
  await runSQL(page, 'DROP DATABASE IF EXISTS gt_e2e_drop')
  await runSQL(page, 'CREATE DATABASE gt_e2e_drop')
  await runSQL(page, 'DROP SCHEMA IF EXISTS gt_e2e_schema CASCADE; CREATE SCHEMA gt_e2e_schema')

  const rail = page.getByRole('navigation', { name: 'Open databases' })
  // The connected database can't be deleted from here.
  const main = rail.getByRole('button', { name: /^Database postgres/ }).first()
  await main.click({ button: 'right' })
  await expect(page.getByRole('menuitem', { name: /Delete database postgres \(connected\)/ })).toBeDisabled()
  await page.getByRole('menuitem', { name: 'Copy database name' }).click()
  await expect.poll(() => clipboard(page)).toBe('postgres')

  // Another database, open in the rail (its own connection): it closes, then goes.
  await openFromPicker(page, 'gt_e2e_drop')
  const tile = rail.getByRole('button', { name: /^Database gt_e2e_drop/ })
  await expect(tile).toBeVisible()
  await tile.click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Delete database gt_e2e_drop…' }).click()
  const dlg = page.getByRole('dialog', { name: 'Delete database' })
  await expect(dlg.getByTestId('drop-preview')).toHaveText('DROP DATABASE "gt_e2e_drop";')
  await dlg.getByLabel('Type the name to confirm').fill('gt_e2e_drop')
  await shot(page, 'drop-database')
  await dlg.getByRole('button', { name: 'Delete database' }).click()
  await expect(dlg).toBeHidden()
  await expect(tile).toHaveCount(0)
  await page.keyboard.press('Control+d')
  const picker = page.getByRole('dialog', { name: 'Open database' })
  await expect(picker.getByRole('option', { name: /^gt_e2e_drop/ })).toHaveCount(0)

  // A schema, from the picker's list.
  await picker.getByRole('option', { name: /Delete a database or schema/ }).click()
  const choose = page.getByRole('dialog', { name: /^Delete/ })
  await choose.getByLabel('Database to delete').selectOption({ label: 'gt_e2e_schema — schema in postgres' })
  await expect(choose.getByTestId('drop-preview')).toHaveText('DROP SCHEMA "gt_e2e_schema" RESTRICT;')
  await choose.getByLabel('Type the name to confirm').fill('gt_e2e_schema')
  await choose.getByRole('button', { name: 'Delete schema' }).click()
  await expect(choose).toBeHidden()
  await page.keyboard.press('Control+d')
  await expect(picker.getByRole('option', { name: /^gt_e2e_schema/ })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.close()
  expect(errors).toEqual([])
})
