import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'

// The query editor marks the whole statement that Run would run, not just
// the cursor's line, and follows the cursor to the next statement.

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

test('the editor highlights the statement under the cursor', async ({ browser }) => {
  const page = await browser.newPage()
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(join(process.env.GOTABLE_TMP!, 'editor.sqlite'))
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByText('Jump anywhere')).toBeVisible()

  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last()
  const content = editor.locator('.cm-content')
  const marked = editor.locator('.cm-line.cm-statement-highlight')
  const markedText = async () => (await marked.allTextContents()).join('\n')
  await content.click()
  await page.keyboard.type('SELECT\n*\nFROM\nsqlite_master;')
  // All four lines of the statement, and their line numbers.
  await expect(marked).toHaveCount(4)
  await expect(editor.locator('.cm-gutterElement.cm-statement-gutter')).toHaveCount(4)
  // The line below isn't the statement: nothing is marked, and Run says so.
  await page.keyboard.press('Enter')
  await expect(marked).toHaveCount(0)
  // Back up onto its last line, it is marked at once.
  await page.keyboard.press('ArrowUp')
  await expect(marked).toHaveCount(4)
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(marked).toHaveCount(0)
  await page.keyboard.press('Control+Enter')
  await expect(page.getByRole('status').filter({ hasText: 'No statement under the cursor' })).toBeVisible()
  await page.keyboard.type('SELECT 1 AS one,\n2 AS two;')
  await expect(marked).toHaveCount(2)
  expect(await markedText()).toContain('SELECT 1 AS one,')
  await shot(page, 'editor-statement')
  // Blank lines between the two statements: nothing marked (the screenshot's case).
  await page.keyboard.press('Control+Home')
  for (let i = 0; i < 4; i++) await page.keyboard.press('ArrowDown') // line 5, blank
  await page.keyboard.press('Enter') // now a blank line with blank lines around it
  await expect(marked).toHaveCount(0)
  await shot(page, 'editor-between')
  // Back in the first statement, it is marked again; Run runs that one.
  await editor.locator('.cm-line', { hasText: 'sqlite_master' }).click()
  await expect(marked).toHaveCount(4)
  await page.keyboard.press('Control+Enter')
  await expect(page.getByRole('columnheader', { name: /^tbl_name/ })).toBeVisible()
  // With a selection, the selection is what runs: no statement is marked.
  await page.keyboard.press('Shift+Home')
  await expect(marked).toHaveCount(0)
  await page.close()
  expect(errors).toEqual([])
})
