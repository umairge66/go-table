import { expect, test } from '@playwright/test'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// Opening a table puts the keyboard on its first row: arrow keys work
// without clicking into the grid first.

test('opening a table focuses its first row', async ({ browser }) => {
  const file = join(process.env.GOTABLE_TMP!, 'focus.sqlite')
  const db = new DatabaseSync(file)
  db.exec(`DROP TABLE IF EXISTS fruit; CREATE TABLE fruit (id INTEGER PRIMARY KEY, name TEXT);
    INSERT INTO fruit (name) VALUES ('apple'), ('banana'), ('cherry');
    DROP TABLE IF EXISTS empty; CREATE TABLE empty (id INTEGER PRIMARY KEY);
    DROP TABLE IF EXISTS veg; CREATE TABLE veg (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO veg (name) VALUES ('leek'), ('kale');`)
  db.close()

  const page = await browser.newPage()
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(file)
  await form.getByRole('button', { name: 'Save & connect' }).click()
  const sidebar = page.getByRole('navigation', { name: 'Tables' })

  // A click on the table in the sidebar: the first row is selected and has the keyboard.
  await sidebar.getByRole('button', { name: 'fruit', exact: true }).click()
  const grid = page.getByTestId('data-fruit').getByTestId('grid')
  const current = grid.locator('.outline-accent')
  await expect(current).toHaveText('1')
  await expect.poll(() => grid.evaluate((g) => g.contains(document.activeElement))).toBe(true)
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('ArrowDown')
  await expect(current).toHaveText('banana')

  // Switching back to an open table keeps the row it was on.
  await sidebar.getByRole('button', { name: 'veg', exact: true }).click()
  await expect(page.getByTestId('data-veg').getByTestId('grid').locator('.outline-accent')).toHaveText('1')
  await page.getByRole('tab', { name: /fruit/ }).click()
  await expect(current).toHaveText('banana')

  // Ctrl+P opens one the same way.
  await page.keyboard.press('Control+w')
  await page.keyboard.press('Control+w')
  await page.keyboard.press('Control+p')
  await page.getByRole('dialog', { name: /^Tables in / }).getByLabel('Search commands').fill('veg')
  await page.keyboard.press('Enter')
  const veg = page.getByTestId('data-veg').getByTestId('grid')
  await expect(veg.locator('.outline-accent')).toHaveText('1')
  await page.keyboard.press('ArrowDown')
  await expect(veg.locator('.outline-accent')).toHaveText('2')

  // An empty table has no row to focus; nothing breaks.
  await sidebar.getByRole('button', { name: 'empty', exact: true }).click()
  await expect(page.getByTestId('data-empty')).toBeVisible()

  // Typing in the table filter isn't interrupted: Enter opens the first match, and the typing stays put.
  await page.keyboard.press('Control+w')
  await page.getByLabel('Filter tables').fill('fru')
  await page.getByLabel('Filter tables').press('Enter')
  await expect(page.getByTestId('data-fruit').getByTestId('grid').locator('.outline-accent')).toHaveCount(0)
  await expect(page.getByLabel('Filter tables')).toBeFocused()
  await page.close()
  expect(errors).toEqual([])
})
