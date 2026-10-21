import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// The tests run in order against one server and share its state.
test.describe.configure({ mode: 'serial' })

let page: Page

const errors: string[] = []

// Headless Chrome has no system "Save As" dialog: stand in for it and keep
// what the app writes, so tests can check the saved files.
const fakeSavePicker = () => {
  const w = window as unknown as Record<string, unknown>
  w.__saved = [] as { name: string; chunks: unknown[] }[]
  w.__picker = [] as string[]
  w.showSaveFilePicker = async (o: { suggestedName: string }) => {
    ;(w.__picker as string[]).push(o.suggestedName)
    if (w.__cancelNextSave) { w.__cancelNextSave = false; throw new DOMException('The user aborted a request.', 'AbortError') }
    const chunks: unknown[] = []
    const done = () => (w.__saved as { name: string; chunks: unknown[] }[]).push({ name: o.suggestedName, chunks })
    return {
      name: o.suggestedName,
      createWritable: async () => {
        const ws = new WritableStream({ write(c) { chunks.push(c) }, close() { done() } }) as WritableStream & Record<string, unknown>
        ws.write = async (d: unknown) => { chunks.push(d) }
        ws.close = async () => { done() }
        return ws
      },
    }
  }
}

async function savedFiles(): Promise<{ name: string; text: string }[]> {
  return page.evaluate(async () => {
    const saved = (window as unknown as { __saved: { name: string; chunks: BlobPart[] }[] }).__saved
    return Promise.all(saved.map(async (f) => ({ name: f.name, text: await new Blob(f.chunks).text() })))
  })
}

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage()
  await page.addInitScript(fakeSavePicker)
  page.on('dialog', (d) => { errors.push(`browser dialog shown: ${d.type()} ${d.message()}`); d.dismiss() })
  page.on('pageerror', (e) => errors.push(e.message))
  // 4xx responses are expected (validation, failing SQL) and logged by Chrome.
  page.on('console', (m) => { if (m.type() === 'error' && !m.text().startsWith('Failed to load resource')) errors.push(m.text()) })
  await page.goto(process.env.GOTABLE_URL!)
})

test.afterAll(() => {
  expect(errors, 'no browser errors').toEqual([])
})

const form = () => page.getByLabel('Connection form')
const sidebar = () => page.getByRole('navigation', { name: 'Tables' })
const grid = () => page.getByTestId('grid')

async function runSQL(sql: string) {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(sql)
  await page.keyboard.press('Control+Shift+Enter')
}

test('welcome screen and creating a SQLite connection', async () => {
  await expect(page.getByText('Welcome to GoTable')).toBeVisible()
  await page.keyboard.press('Control+n')
  await form().getByRole('radio', { name: 'SQLite' }).click()
  await expect(form().getByText('Database file')).toBeVisible()
  // Validation is explained inline.
  await form().getByRole('button', { name: 'Test' }).click()
  await expect(form().getByRole('status')).toContainText('database file is required')

  await form().getByPlaceholder('/home/you/data/app.sqlite').fill(join(process.env.GOTABLE_TMP!, 'shop.sqlite'))
  await form().getByRole('radio', { name: 'Staging' }).click()
  await form().getByRole('button', { name: 'Test' }).click()
  await expect(form().getByRole('status')).toContainText('Connected successfully')
  await form().getByRole('button', { name: 'Save & connect' }).click()

  // Workspace opens with the empty state and the environment shown.
  await expect(page.getByText('Jump anywhere')).toBeVisible()
  await expect(page.locator('header').getByText('Staging')).toBeVisible()
})

test('SQL editor creates tables and the sidebar refreshes', async () => {
  await runSQL(`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT, balance REAL);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 450)
INSERT INTO customers (name, city, balance) SELECT 'Customer ' || i, CASE i % 4 WHEN 0 THEN 'Berlin' WHEN 1 THEN 'Paris' WHEN 2 THEN 'Rome' ELSE NULL END, i * 1.5 FROM n;
CREATE VIEW berliners AS SELECT name FROM customers WHERE city = 'Berlin';
SELECT COUNT(*) AS n FROM customers;`)
  await expect(page.getByRole('tab', { name: /Result 1/ })).toBeVisible()
  await expect(grid().last().getByRole('gridcell').first()).toHaveText('450')
  await page.getByRole('tab', { name: 'Messages' }).click()
  await expect(page.getByTestId('messages')).toContainText('450 row(s) affected')
  await expect(sidebar().getByRole('button', { name: 'customers', exact: true })).toBeVisible()
  await expect(sidebar().getByRole('button', { name: 'berliners' })).toBeVisible()
})

test('errors are explained in plain language', async () => {
  await runSQL('SELECT * FROM nope')
  await expect(page.getByTestId('messages').last()).toContainText('Table not found')
  await expect(page.getByTestId('messages').last()).toContainText('no such table')
})

test('SQL editor beautifies the query or the selection', async () => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill("select id,name from customers where city='Berlin' and balance>100 order by name")
  await page.getByRole('button', { name: 'Format' }).last().click()
  await expect(editor.locator('.cm-line')).toHaveText(['SELECT', '  id,', '  name', 'FROM', '  customers', 'WHERE',
    "  city = 'Berlin'", '  AND balance > 100', 'ORDER BY', '  name'])
  // One undo restores the original text.
  await page.keyboard.press('Control+z')
  await expect(editor).toHaveText("select id,name from customers where city='Berlin' and balance>100 order by name")

  // Ctrl+I formats only the selection.
  await editor.fill('select 1;\nselect a,b from t')
  await page.keyboard.press('Control+End')
  await page.keyboard.press('Shift+Home')
  await page.keyboard.press('Control+i')
  await expect(editor.locator('.cm-line')).toHaveText(['select 1;', 'SELECT', '  a,', '  b', 'FROM', '  t'])

  // Invalid SQL is left untouched and explained.
  await editor.fill("select 'oops")
  await page.keyboard.press('Control+i')
  await expect(page.getByRole('status').filter({ hasText: "Couldn't format this SQL" })).toBeVisible()
  await expect(editor).toHaveText("select 'oops")
  await page.keyboard.press('Control+w')
})

test('browsing: paging, quick search, filters and sorting', async () => {
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await expect(view.getByText('1–300 of 450 rows')).toBeVisible()
  await view.getByRole('button', { name: 'Next page' }).click()
  await expect(view.getByText('301–450 of 450 rows')).toBeVisible()

  await view.getByLabel('Quick search').fill('Customer 44')
  await view.getByLabel('Quick search').press('Enter')
  // 44, 440..449 = 11 rows
  await expect(view.getByText('1–11 of 11 rows')).toBeVisible()
  await view.getByLabel('Quick search').press('Escape')
  await expect(view.getByText('1–300 of 450 rows')).toBeVisible()

  await view.getByRole('button', { name: /^Filter/ }).click()
  await view.getByLabel('Filter column').selectOption('city')
  await view.getByLabel('Filter operator').selectOption('IS NULL')
  await view.getByRole('button', { name: /^Apply/ }).click()
  await expect(view.getByText('1–112 of 112 rows')).toBeVisible()
  await expect(view.getByRole('button', { name: 'Filter · 1' })).toBeVisible()
  await view.getByRole('button', { name: 'Clear' }).click()
  await expect(view.getByText('1–300 of 450 rows')).toBeVisible()

  // Sort by balance descending (two clicks on the header).
  await view.getByRole('columnheader', { name: /balance/ }).click()
  await view.getByRole('columnheader', { name: /balance/ }).click()
  await expect(view.getByRole('gridcell').nth(0)).toHaveText('450')
  await view.getByRole('columnheader', { name: /balance/ }).click() // back to unsorted

  // Rows per page is picked in the footer.
  await view.getByLabel('Rows per page').selectOption('100')
  await expect(view.getByText('1–100 of 450 rows')).toBeVisible()
  await expect(view.getByText('1 / 5')).toBeVisible()
  await view.getByLabel('Rows per page').selectOption('300')
  await expect(view.getByText('1–300 of 450 rows')).toBeVisible()
})

test('editing: inline edit, add, delete, review and commit', async () => {
  const view = page.getByTestId('data-customers')
  await expect(view.getByRole('gridcell').nth(0)).toHaveText('1')
  // Double-click the name of row 1 and append text.
  await view.getByRole('gridcell').nth(1).dblclick()
  await page.keyboard.type(' VIP')
  await page.keyboard.press('Enter')
  await expect(view.getByRole('gridcell').nth(1)).toHaveText('Customer 1 VIP')
  // Set row 2's city to NULL via the context menu.
  await view.getByRole('gridcell').nth(6).click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Set to NULL' }).click()
  // Delete row 3 with the keyboard.
  await view.getByRole('gridcell').nth(8).click()
  await page.keyboard.press('Delete')
  // Add a row and type into it.
  await view.getByRole('button', { name: 'Row', exact: true }).click()
  // Adding a row scrolls to it and opens the editor on its first non-key column.
  await expect(page.getByLabel('Edit cell')).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(view.getByText('4 unsaved changes')).toBeVisible()

  await page.keyboard.press('Control+s')
  const dialog = page.getByRole('dialog', { name: 'Review changes' })
  await expect(dialog).toContainText('UPDATE "customers" SET "name" = \'Customer 1 VIP\'')
  await expect(dialog).toContainText('DELETE FROM "customers" WHERE "id" = \'3\'')
  // The new row has no values, so the NOT NULL name makes the commit fail and roll back.
  await dialog.getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'A required column is empty' })).toBeVisible()
  // Nothing was written; the edits are still pending.
  await page.keyboard.press('Escape')
  await expect(view.getByText('4 unsaved changes')).toBeVisible()

  // Add row put us into the new row's first editable cell; go back there via
  // the keyboard (End jumps to the last row) and type its name.
  await view.getByRole('gridcell').nth(0).click()
  await page.keyboard.press('End')
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('Enter')
  await page.keyboard.type('New customer')
  await page.keyboard.press('Enter')
  await expect(view.getByText('4 unsaved changes')).toBeVisible()
  await page.keyboard.press('Control+s')
  await page.getByRole('dialog', { name: 'Review changes' }).getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: /Saved 4 changes/ })).toBeVisible()
  await expect(view.getByText('unsaved change')).toHaveCount(0)

  await runSQL("SELECT (SELECT name FROM customers WHERE id = 1), (SELECT city IS NULL FROM customers WHERE id = 2), (SELECT COUNT(*) FROM customers WHERE id = 3), (SELECT COUNT(*) FROM customers WHERE name = 'New customer')")
  const cells = grid().last().getByRole('gridcell')
  await expect(cells.nth(0)).toHaveText('Customer 1 VIP')
  await expect(cells.nth(1)).toHaveText('1')
  await expect(cells.nth(2)).toHaveText('0')
  await expect(cells.nth(3)).toHaveText('1')
})

test('query results from one table are editable', async () => {
  // A scratch row to delete, so the table keeps its 450 rows for later tests.
  await runSQL("INSERT INTO customers (id, name) VALUES (1000, 'Scratch');\nSELECT id, name, city FROM customers WHERE id IN (4, 5, 1000) ORDER BY id")
  await expect(page.getByTestId('result-editable').last()).toHaveText('Editable · customers')
  const cells = () => grid().last().getByRole('gridcell')
  await cells().nth(1).dblclick()
  await page.keyboard.type(' edited')
  await page.keyboard.press('Enter')
  await cells().nth(8).click() // the scratch row
  await page.keyboard.press('Delete')
  await expect(page.getByText('2 unsaved changes to customers')).toBeVisible()
  // Running another query asks first.
  await page.getByRole('button', { name: 'Run all' }).last().click()
  await page.getByRole('dialog', { name: 'Unsaved changes in the result' }).getByRole('button', { name: 'Cancel' }).click()
  await page.getByRole('button', { name: 'Review & commit' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review changes' })
  await expect(dialog).toContainText('UPDATE "customers" SET "name" = \'Customer 4 edited\'')
  await expect(dialog).toContainText('DELETE FROM "customers" WHERE "id" = \'1000\'')
  await dialog.getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Saved 2 changes' })).toBeVisible()
  // The SELECT runs again and shows the rows as stored.
  await expect(cells()).toHaveCount(6)
  await expect(cells().nth(1)).toHaveText('Customer 4 edited')
  await expect(page.getByText('unsaved change')).toHaveCount(0)

  // Results that can't be mapped back to rows stay read-only.
  for (const sql of ['SELECT COUNT(*) FROM customers', 'SELECT name FROM customers LIMIT 3']) {
    await page.keyboard.press('Control+w')
    await runSQL(sql)
    await expect(page.getByTestId('result-readonly').last()).toBeVisible()
    await cells().first().dblclick()
    await expect(page.getByLabel('Edit cell')).toHaveCount(0)
  }
  await page.keyboard.press('Control+w')
})

test('cells get an editor for their type: calendar, time, choices', async () => {
  await runSQL(`CREATE TABLE events (id INTEGER PRIMARY KEY, day DATE, at DATETIME, starts TIME, done BOOLEAN NOT NULL);
INSERT INTO events VALUES (1, '2024-03-15', '2024-03-15 10:20:30', '09:00:00', 0);`)
  await page.keyboard.press('Control+w')
  await sidebar().getByRole('button', { name: 'events', exact: true }).click()
  const view = page.getByTestId('data-events')
  const cells = view.getByRole('gridcell')
  const picker = page.getByTestId('cell-picker')

  // DATE: a calendar on the value's month; picking a day finishes the edit.
  await cells.nth(1).dblclick()
  await expect(picker).toContainText('March 2024')
  await picker.getByRole('gridcell', { name: '2024-03-20' }).click()
  await expect(picker).toHaveCount(0)
  await expect(cells.nth(1)).toHaveText('2024-03-20')

  // DATETIME: calendar plus time fields, then Done.
  await cells.nth(2).dblclick()
  await picker.getByRole('gridcell', { name: '2024-03-01' }).click()
  await picker.getByLabel('Hours').fill('14')
  await expect(page.getByLabel('Edit cell')).toHaveValue('2024-03-01 14:20:30')
  await picker.getByRole('button', { name: 'Done' }).click()
  await expect(cells.nth(2)).toHaveText('2024-03-01 14:20:30')

  // TIME: arrow keys step a field; Enter finishes.
  await cells.nth(3).dblclick()
  await picker.getByLabel('Minutes').focus()
  await page.keyboard.press('ArrowUp')
  await page.keyboard.press('Enter')
  await expect(cells.nth(3)).toHaveText('09:01:00')

  // BOOLEAN: a list; not nullable, so no NULL choice.
  await cells.nth(4).dblclick()
  await expect(picker.getByRole('option')).toHaveText(['1 (true)', '0 (false)'])
  await picker.getByRole('option', { name: '1 (true)' }).click()
  await expect(cells.nth(4)).toHaveText('1')

  // Typing still works, and Escape cancels.
  await cells.nth(1).dblclick()
  await page.keyboard.press('Escape')
  await expect(cells.nth(1)).toHaveText('2024-03-20')

  await page.keyboard.press('Control+s')
  await page.getByRole('dialog', { name: 'Review changes' }).getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Saved 1 change' })).toBeVisible()
  await expect(cells.nth(2)).toHaveText('2024-03-01 14:20:30')
  await page.keyboard.press('Control+w')
})

test('structure: edit columns, indexes, foreign keys and the table itself', async () => {
  await runSQL(`CREATE TABLE makers (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE gadgets (id INTEGER PRIMARY KEY, name TEXT, price REAL, maker_id INTEGER);
INSERT INTO makers VALUES (1, 'Acme');
INSERT INTO gadgets VALUES (1, 'Widget', 9.5, 1), (2, 'Gizmo', 3, 1);`)
  await page.keyboard.press('Control+w')
  await sidebar().getByRole('button', { name: 'gadgets', exact: true }).click()
  await page.getByTestId('data-gadgets').getByRole('tab', { name: 'Structure' }).click()
  const st = page.getByTestId('structure')
  const review = async () => {
    await expect(page.getByTestId('alter-review')).toBeVisible()
    await page.getByRole('button', { name: 'Run', exact: true }).click()
    await expect(page.getByTestId('alter-review')).toHaveCount(0)
  }

  // Add a column.
  await st.getByRole('button', { name: 'Add column' }).click()
  await page.getByLabel('Column name').fill('sku')
  await expect(page.getByTestId('alter-sql')).toContainText('ADD COLUMN "sku"')
  await page.getByRole('button', { name: 'Add column', exact: true }).last().click()
  await expect(st.getByTestId('column-sku')).toBeVisible()

  // Rename price and make it NOT NULL (SQLite rebuilds the table).
  await st.getByRole('button', { name: 'Edit price' }).click()
  await expect(page.getByTestId('alter-sql')).toHaveText('No changes yet.')
  await page.getByLabel('Column name').fill('cost')
  await page.getByRole('dialog').getByText('Allow NULL').click()
  await expect(page.getByTestId('alter-sql')).toContainText('"cost" REAL NOT NULL')
  await page.getByRole('button', { name: 'Save column' }).click()
  await expect(st.getByTestId('column-cost')).toContainText('NOT NULL')
  await expect(st.getByTestId('column-price')).toHaveCount(0)

  // An index from the column menu, renamed, then dropped.
  await st.getByRole('button', { name: 'More for name' }).click()
  await page.getByRole('menuitem', { name: 'Index', exact: true }).click()
  await review()
  await expect(st.getByTestId('index-gadgets_name_idx')).toBeVisible()
  await st.getByRole('button', { name: 'Rename gadgets_name_idx' }).click()
  await page.getByLabel('New index name').fill('by_name')
  await page.getByRole('button', { name: 'Rename', exact: true }).last().click()
  await expect(st.getByTestId('index-by_name')).toBeVisible()
  await st.getByRole('button', { name: 'Drop by_name' }).click()
  await review()
  await expect(st.getByTestId('index-by_name')).toHaveCount(0)

  // Several columns at once: a unique index on (name, sku).
  await st.getByLabel('Select name').check()
  await st.getByLabel('Select sku').check()
  await st.getByRole('toolbar', { name: 'Selected columns' }).getByRole('button', { name: 'Unique' }).click()
  await review()
  await expect(st.getByTestId('index-gadgets_name_sku_key')).toContainText('UNIQUE')

  // A foreign key to makers.
  await st.getByRole('button', { name: 'Add foreign key' }).click()
  await page.getByLabel('Referenced table').selectOption('makers')
  await page.getByLabel('Column 1', { exact: true }).selectOption('maker_id')
  await expect(page.getByLabel('Referenced column 1')).toHaveValue('id')
  await page.getByLabel('On delete').selectOption('CASCADE')
  await page.getByRole('button', { name: 'Add foreign key', exact: true }).last().click()
  await expect(st.getByRole('region', { name: 'Foreign keys' })).toContainText('makers(id)')
  await expect(st.getByRole('region', { name: 'Foreign keys' })).toContainText('CASCADE')

  // Drop a column, rename the table: the tab follows it.
  await st.getByRole('button', { name: 'Drop sku' }).click()
  await review()
  await expect(st.getByTestId('column-sku')).toHaveCount(0)
  await st.getByLabel('New table name').fill('gizmos')
  await st.getByRole('button', { name: 'Rename', exact: true }).click()
  await review()
  await expect(page.getByRole('tablist', { name: 'Open tabs' }).getByRole('tab', { selected: true })).toContainText('gizmos')
  await expect(sidebar().getByRole('button', { name: 'gizmos', exact: true })).toBeVisible()
  await expect(page.getByTestId('data-gizmos').getByTestId('column-cost')).toBeVisible()

  await runSQL('SELECT name, cost, maker_id FROM gizmos ORDER BY id')
  await expect(grid().last().getByRole('gridcell').nth(1)).toHaveText('9.5')
  await runSQL("SELECT sql FROM sqlite_master WHERE name = 'gizmos'")
  await expect(grid().last().getByRole('gridcell').first()).toContainText('ON DELETE CASCADE')
  await page.keyboard.press('Control+w')
  await page.keyboard.press('Control+w')
})

test('row inspector, structure and definition', async () => {
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await view.getByRole('gridcell').nth(2).click()
  const inspector = view.getByLabel('Row inspector')
  await expect(inspector).toContainText('balance')
  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByRole('cell', { name: 'NOT NULL' })).toBeVisible()
  await view.getByRole('tab', { name: 'Definition' }).click()
  await expect(view.getByTestId('sql-editor')).toContainText('CREATE TABLE customers')
  await view.getByRole('tab', { name: 'Data' }).click()
})

test('command palette opens tables; views are read-only', async () => {
  await page.keyboard.press('Control+k')
  await page.getByLabel('Search commands').fill('berl')
  await page.keyboard.press('Enter')
  const view = page.getByTestId('data-berliners')
  await expect(view.getByText('of 112 rows')).toBeVisible()
  await expect(view.getByRole('button', { name: 'Row', exact: true })).toHaveCount(0)
  await view.getByRole('gridcell').first().dblclick()
  await expect(page.getByLabel('Edit cell')).toHaveCount(0)
})

test('export downloads a CSV of the filtered table', async () => {
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await view.getByLabel('Quick search').fill('Customer 10')
  await view.getByLabel('Quick search').press('Enter')
  await expect(view.getByText('1–11 of 11 rows')).toBeVisible() // 10 and 100–109
  await view.getByRole('button', { name: 'Export' }).click()
  // Cancelling the Save As dialog starts nothing.
  await page.evaluate(() => { (window as unknown as Record<string, unknown>).__cancelNextSave = true })
  await page.getByRole('button', { name: 'Export CSV…' }).click()
  await expect(page.getByRole('dialog', { name: 'Export customers' })).toBeVisible()
  expect(await savedFiles()).toHaveLength(0)
  await page.getByRole('button', { name: 'Export CSV…' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Export saved' })).toBeVisible()
  const files = await savedFiles()
  expect(files.map((f) => f.name)).toEqual(['customers.csv'])
  expect(files[0].text.split('\n')[0]).toBe('id,name,city,balance')
  expect(files[0].text.trim().split('\n')).toHaveLength(1 + 11)
})

test('structure: add a column with a live SQL preview', async () => {
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await view.getByRole('tab', { name: 'Structure' }).click()
  await view.getByRole('button', { name: 'Add column' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add column' })
  const sql = dialog.getByTestId('alter-sql')

  // Existing names are flagged.
  await dialog.getByLabel('Column name').fill('city')
  await expect(dialog.getByText('A column with this name already exists.')).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Add column' })).toBeDisabled()

  // SQLite's NOT NULL rule is explained before anything runs.
  await dialog.getByLabel('Column name').fill('vip')
  await dialog.getByLabel('Column type').selectOption('TEXT')
  await dialog.getByRole('switch').click() // disallow NULL
  await expect(sql).toContainText('SQLite can only add a NOT NULL column if it has a default value')
  await expect(dialog.getByRole('button', { name: 'Add column' })).toBeDisabled()

  await dialog.getByRole('radio', { name: 'Value' }).click()
  await dialog.getByLabel('Default value').fill('no')
  await expect(sql).toHaveText(`ALTER TABLE "customers" ADD COLUMN "vip" TEXT NOT NULL DEFAULT 'no';`)
  await dialog.getByRole('button', { name: 'Add column' }).click()

  await expect(page.getByRole('status').filter({ hasText: 'Column “vip” added' })).toBeVisible()
  await expect(dialog).toHaveCount(0)
  await expect(view.getByRole('row', { name: /vip/ })).toBeVisible()
  // The data view shows the new column with its default for existing rows.
  await view.getByRole('tab', { name: 'Data' }).click()
  await expect(view.getByRole('columnheader', { name: /vip/ })).toBeVisible()
  await expect(view.locator('[data-row="0"] [role=gridcell]').last()).toHaveText('no')
})

test('database export (SQL dump) with progress, then importing it back', async () => {
  await page.getByRole('button', { name: 'Import / Export database' }).click()
  const dialog = page.getByRole('dialog', { name: 'Export database' })
  await expect(dialog.getByLabel('Tables to export')).toContainText('customers')
  await dialog.getByRole('switch').nth(2).click() // DROP existing tables first
  await dialog.getByRole('button', { name: /^Export \d+ tables…/ }).click()
  await expect(dialog.getByTestId('job-progress')).toHaveAttribute('data-status', 'done')
  await expect(dialog.getByTestId('job-progress')).toContainText(/Saved to shop-\d{4}-\d{2}-\d{2}-\d{4}\.sql/)
  const file = (await savedFiles()).at(-1)!
  expect(file.name).toMatch(/^shop-\d{4}-\d{2}-\d{2}-\d{4}\.sql$/)
  const dump = file.text
  expect(dump).toContain('DROP TABLE IF EXISTS "customers"')
  expect(dump).toContain('CREATE TABLE customers')
  expect(dump).toContain('CREATE VIEW berliners')
  expect(dump).toMatch(/INSERT INTO "customers" \("id", "name", "city", "balance", "vip"\) VALUES/)

  // Import the dump into the same database: everything is replaced, nothing lost.
  await dialog.getByRole('tab', { name: 'Import' }).click()
  const importDialog = page.getByRole('dialog', { name: 'Import into database' })
  await importDialog.getByTestId('file-input').setInputFiles({ name: 'dump.sql', mimeType: 'application/sql', buffer: Buffer.from(dump) })
  await expect(importDialog.getByTestId('picked-file')).toContainText('dump.sql')
  await importDialog.getByRole('button', { name: 'Start import' }).click()
  await expect(importDialog.getByTestId('job-progress')).toHaveAttribute('data-status', 'done')
  await expect(importDialog.getByTestId('job-progress')).not.toContainText('failed')
  await page.keyboard.press('Escape')
  await runSQL('SELECT COUNT(*), SUM(id) FROM customers')
  await expect(grid().last().getByRole('gridcell').nth(0)).toHaveText('450')
})

test('importing a SQL file that has an error: skip and continue', async () => {
  let sql = 'CREATE TABLE imported_notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL);\n'
  for (let i = 1; i <= 2000; i++) sql += `INSERT INTO imported_notes (body) VALUES ('note ${i}; with a semicolon');\n`
  sql += 'INSERT INTO imported_notes (body) VALUES (NULL);\nINSERT INTO imported_notes (body) VALUES (\'after the error\');\n'
  await page.keyboard.press('Control+k')
  await page.getByLabel('Search commands').fill('Import SQL')
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Import into database' })
  await dialog.getByTestId('file-input').setInputFiles({ name: 'notes.sql', mimeType: 'application/sql', buffer: Buffer.from(sql) })
  await dialog.getByText('Skip it and keep going').click()
  await dialog.getByRole('button', { name: 'Start import' }).click()
  const progress = dialog.getByTestId('job-progress')
  await expect(progress).toHaveAttribute('data-status', 'done')
  await expect(progress).toContainText('2,003 statements')
  await expect(progress).toContainText('1 failed')
  await expect(dialog.getByTestId('job-errors')).toContainText('Line 2,002:')
  await expect(dialog.getByTestId('job-errors')).toContainText('NOT NULL')
  await page.keyboard.press('Escape')
  await expect(sidebar().getByRole('button', { name: 'imported_notes' })).toBeVisible()
  await runSQL('SELECT COUNT(*) FROM imported_notes')
  await expect(grid().last().getByRole('gridcell').nth(0)).toHaveText('2001')
  // The finished tasks are listed in the status bar.
  await page.getByTestId('jobs-indicator').first().click()
  await expect(page.getByRole('dialog', { name: 'Imports & exports' }).getByTestId('job-progress')).toHaveCount(4) // table CSV, database export, two imports
  await page.keyboard.press('Escape')
})

test('CSV import into a table with automatic column mapping', async () => {
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await view.getByRole('button', { name: 'Import' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import CSV into customers' })
  const csv = 'Name;City;Balance\n"Smith; Jane";Oslo;12.5\nLee;;0\nNULL person;NULL;1\n'
  await dialog.getByTestId('file-input').setInputFiles({ name: 'people.csv', mimeType: 'text/csv', buffer: Buffer.from(csv) })
  // Delimiter is detected and columns are matched by name.
  await expect(dialog.getByLabel('CSV delimiter')).toHaveValue(';')
  await expect(dialog.getByLabel('Source for name')).toHaveValue('0')
  await expect(dialog.getByLabel('Source for city')).toHaveValue('1')
  await expect(dialog.getByLabel('Source for balance')).toHaveValue('2')
  await expect(dialog.getByLabel('Source for vip')).toHaveValue('-1') // uses its default
  await expect(dialog.getByLabel('Column mapping')).toContainText('Smith; Jane')
  await dialog.getByRole('button', { name: 'Import into customers' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Imported 3 rows' })).toBeVisible()
  await expect(dialog).toHaveCount(0)
  await runSQL("SELECT COUNT(*), SUM(city IS NULL), MAX(vip) FROM customers WHERE name IN ('Smith; Jane', 'Lee', 'NULL person')")
  const cells = grid().last().getByRole('gridcell')
  await expect(cells.nth(0)).toHaveText('3')
  await expect(cells.nth(1)).toHaveText('2') // empty and NULL text both became NULL
  await expect(cells.nth(2)).toHaveText('no')
})

test('unsaved edits: in-app dialog on refresh, no browser alert or reload', async () => {
  await page.evaluate(() => { (window as unknown as Record<string, unknown>).__notReloaded = true })
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await view.getByRole('gridcell').nth(1).dblclick()
  await page.keyboard.type('!')
  await page.keyboard.press('Enter')
  await expect(view.getByText('1 unsaved change')).toBeVisible()

  // F5 asks inside the app; Cancel keeps the edit.
  await page.keyboard.press('F5')
  const ask = page.getByRole('dialog', { name: 'Unsaved changes in customers' })
  await expect(ask).toBeVisible()
  await ask.getByRole('button', { name: 'Cancel' }).click()
  await expect(view.getByText('1 unsaved change')).toBeVisible()

  // Ctrl+R offers to commit instead.
  await page.keyboard.press('Control+r')
  await ask.getByRole('button', { name: 'Review & commit…' }).click()
  await expect(page.getByRole('dialog', { name: 'Review changes' })).toBeVisible()
  await page.keyboard.press('Escape')

  // Discard throws the edit away and refreshes.
  await page.keyboard.press('F5')
  await ask.getByRole('button', { name: 'Discard changes' }).click()
  await expect(view.getByText('unsaved change')).toHaveCount(0)

  // Closing a tab with edits asks the same way.
  await view.getByRole('gridcell').nth(1).dblclick()
  await page.keyboard.type('?')
  await page.keyboard.press('Enter')
  await page.getByRole('button', { name: 'Close customers' }).click()
  const closeAsk = page.getByRole('dialog', { name: 'Close customers?' })
  await closeAsk.getByRole('button', { name: 'Cancel' }).click()
  await expect(view.getByText('1 unsaved change')).toBeVisible()
  await view.getByRole('button', { name: 'Discard' }).click()

  // The window never reloaded and no browser dialog appeared (checked in afterAll).
  expect(await page.evaluate(() => (window as unknown as Record<string, unknown>).__notReloaded)).toBe(true)
})

test('browser behaviour is switched off', async () => {
  const prevented = (type: 'contextmenu' | 'keydown', init: Record<string, unknown> = {}) => page.evaluate(([t, i]) => {
    const target = document.querySelector('[role=tablist]') ?? document.body
    const ev = t === 'contextmenu' ? new MouseEvent('contextmenu', { bubbles: true, cancelable: true }) : new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...(i as object) })
    target.dispatchEvent(ev)
    return ev.defaultPrevented
  }, [type, init] as const)
  expect(await prevented('contextmenu')).toBe(true)
  expect(await prevented('keydown', { key: 's', ctrlKey: true })).toBe(true) // no "Save page as"
  expect(await prevented('keydown', { key: 'u', ctrlKey: true })).toBe(true) // no view-source
  expect(await prevented('keydown', { key: 'h', ctrlKey: true })).toBe(true) // no history
  expect(await prevented('keydown', { key: 'ArrowLeft', altKey: true })).toBe(true) // no history back
  expect(await prevented('keydown', { key: 'a' })).toBe(false) // normal typing untouched
  // Text fields keep their copy/paste menu.
  const inField = await page.evaluate(() => {
    const el = document.querySelector('input')!
    const ev = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    el.dispatchEvent(ev)
    return ev.defaultPrevented
  })
  expect(inField).toBe(false)
})

test('sidebar: select all / several tables, export or delete them', async () => {
  await runSQL(`CREATE TABLE t_parent (id INTEGER PRIMARY KEY);
CREATE TABLE t_child (id INTEGER PRIMARY KEY, parent INT REFERENCES t_parent(id));
CREATE TABLE t_other (x INT);
INSERT INTO t_parent VALUES (1); INSERT INTO t_child VALUES (1, 1);`)
  await expect(sidebar().getByTestId('sidebar-t_other')).toBeVisible()
  const bar = page.getByRole('toolbar', { name: 'Selected tables' })

  // Ctrl+A selects every table listed; Esc clears.
  await sidebar().getByTestId('sidebar-t_other').click()
  await page.keyboard.press('Control+a')
  const total = await sidebar().locator('[data-testid^="sidebar-"]').count()
  await expect(bar).toContainText(`${total} selected`)
  await page.keyboard.press('Escape')
  await expect(bar).toHaveCount(0)

  // With a filter, Ctrl+A selects only the matches.
  await page.getByLabel('Filter tables').fill('t_')
  await sidebar().getByTestId('sidebar-t_child').click()
  await page.keyboard.press('Control+a')
  await expect(bar).toContainText('3 selected')

  // Ctrl+click toggles, Shift+click selects a range.
  await sidebar().getByTestId('sidebar-t_other').click({ modifiers: ['Control'] })
  await expect(bar).toContainText('2 selected')
  await sidebar().getByTestId('sidebar-t_child').click()
  await sidebar().getByTestId('sidebar-t_parent').click({ modifiers: ['Shift'] })
  await expect(bar).toContainText('3 selected')

  // Export the selection: the dialog starts with exactly those tables.
  await bar.getByRole('button', { name: 'Export' }).click()
  const exp = page.getByRole('dialog', { name: 'Export database' })
  await expect(exp).toContainText('3 of')
  await expect(exp.getByRole('button', { name: 'Export 3 tables…' })).toBeVisible()
  await page.keyboard.press('Escape')

  // Bulk delete asks for DELETE to be typed and shows the SQL.
  await bar.getByRole('button', { name: 'Delete' }).click()
  const del = page.getByRole('dialog', { name: 'Delete 3 tables?' })
  await expect(del.getByTestId('drop-sql')).toContainText('DROP TABLE "t_child";')
  const go = del.getByRole('button', { name: 'Delete 3 tables' })
  await expect(go).toBeDisabled()
  await del.getByLabel('Type DELETE to confirm').fill('delete')
  await go.click()
  await expect(page.getByRole('status').filter({ hasText: 'Deleted 3 tables' })).toBeVisible()
  await expect(sidebar().getByText('Nothing matches “t_”.')).toBeVisible()
  await expect(page.getByRole('tab', { name: /t_child/ })).toHaveCount(0) // its tab was closed
  await page.getByLabel('Filter tables').fill('')

  // Single delete from the context menu: a dependency is explained, CASCADE fixes it.
  await runSQL(`CREATE TABLE t_parent (id INTEGER PRIMARY KEY);
CREATE TABLE t_child (id INTEGER PRIMARY KEY, parent INT REFERENCES t_parent(id));
INSERT INTO t_parent VALUES (1); INSERT INTO t_child VALUES (1, 1);`)
  await sidebar().getByTestId('sidebar-t_parent').click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Delete table…' }).click()
  const one = page.getByRole('dialog', { name: 'Delete 1 table?' })
  await one.getByRole('button', { name: 'Delete 1 table' }).click()
  await expect(one.getByRole('alert')).toContainText('Other tables or views depend on it')
  await expect(sidebar().getByTestId('sidebar-t_parent')).toBeVisible() // nothing was deleted
  await one.getByRole('switch').click()
  await expect(one.getByTestId('drop-sql')).toContainText('PRAGMA foreign_keys = OFF;')
  await one.getByRole('button', { name: 'Delete 1 table' }).click()
  await expect(one).toHaveCount(0)
  await expect(sidebar().getByTestId('sidebar-t_parent')).toHaveCount(0)
  await expect(sidebar().getByTestId('sidebar-t_child')).toBeVisible()
})

test('database rail: several databases (Ctrl+D), one sidebar each, table picker (Ctrl+P)', async () => {
  const aux = join(process.env.GOTABLE_TMP!, 'archive.sqlite')
  await runSQL(`ATTACH DATABASE '${aux}' AS archive; CREATE TABLE archive.old_orders (id INTEGER PRIMARY KEY, total REAL); CREATE TABLE archive.old_invoices (id INTEGER PRIMARY KEY)`)
  const rail = page.getByRole('navigation', { name: 'Open databases' })
  const mainTile = rail.getByRole('button', { name: 'Database main' })
  const archiveTile = rail.getByRole('button', { name: 'Database archive' })

  // Open a table in main, then open the other database from the rail's +.
  await sidebar().getByTestId('sidebar-customers').click()
  await expect(page.getByTestId('data-customers')).toBeVisible()
  await rail.getByRole('button', { name: 'Open database' }).click()
  const picker = page.getByRole('dialog', { name: 'Open database' })
  await expect(picker.getByText('Attached databases')).toBeVisible()
  await expect(picker.getByRole('option', { name: /main/ }).getByLabel('current')).toBeVisible()
  await expect(picker.getByRole('option', { name: /customers/ })).toHaveCount(0) // only databases here
  await picker.getByLabel('Search commands').fill('arch')
  await page.keyboard.press('Enter')

  // The rail lists both; the table sidebar shows only the active database.
  await expect(archiveTile).toHaveAttribute('aria-pressed', 'true')
  await expect(mainTile).toHaveAttribute('aria-pressed', 'false')
  await expect(sidebar().getByTestId('sidebar-old_orders')).toBeVisible()
  await expect(sidebar().getByTestId('sidebar-customers')).toHaveCount(0)
  await expect(archiveTile).toHaveAttribute('aria-pressed', 'true')
  // The view switched to archive, not the old table.
  await expect(page.getByTestId('db-empty')).toContainText('archive')
  await expect(page.getByTestId('data-customers')).toBeHidden()
  const customersTab = page.getByRole('tab', { name: /customers/ })
  await expect(customersTab).toContainText('main') // tabs show their database

  // Ctrl+P lists only the active database's tables.
  await page.keyboard.press('Control+p')
  const tables = page.getByRole('dialog', { name: 'Tables in archive' })
  await expect(tables.getByRole('option')).toHaveCount(2)
  await tables.getByLabel('Search commands').fill('inv')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('tab', { name: /old_invoices/ })).toHaveAttribute('aria-selected', 'true')

  // Clicking a rail tile switches database; so do Alt+1 / Alt+2 and tabs.
  await mainTile.click()
  await expect(page.getByTestId('data-customers')).toBeVisible()
  await expect(sidebar().getByTestId('sidebar-customers')).toBeVisible()
  await page.keyboard.press('Alt+2')
  await expect(page.getByRole('tab', { name: /old_invoices/ })).toHaveAttribute('aria-selected', 'true') // its last tab
  await page.keyboard.press('Alt+1')
  await expect(mainTile).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('tab', { name: /old_invoices/ }).click()
  await expect(archiveTile).toHaveAttribute('aria-pressed', 'true')

  // Ctrl+D on an open database just focuses it.
  await mainTile.click()
  await page.keyboard.press('Control+d')
  await expect(picker.getByRole('option', { name: /archive/ })).toContainText('open')
  await picker.getByLabel('Search commands').fill('archive')
  await page.keyboard.press('Enter')
  await expect(archiveTile).toHaveAttribute('aria-pressed', 'true')
  await expect(rail.getByRole('button', { name: /^Database / })).toHaveCount(2)

  // Closing a database (× on its tile) closes its tabs.
  await archiveTile.hover()
  await rail.getByRole('button', { name: 'Close archive' }).click()
  await expect(archiveTile).toHaveCount(0)
  await expect(page.getByRole('tab', { name: /old_invoices/ })).toHaveCount(0)
  await expect(mainTile).toHaveAttribute('aria-pressed', 'true')

  // Ctrl+K still searches everything and offers both pickers.
  await page.keyboard.press('Control+k')
  const all = page.getByRole('dialog', { name: 'Command palette' })
  await expect(all.getByRole('option', { name: /Open database…/ })).toBeVisible()
  await expect(all.getByRole('option', { name: /Go to table…/ })).toBeVisible()
  await page.keyboard.press('Escape')
})

test('foreign key cells open the referenced row', async () => {
  await runSQL(`CREATE TABLE purchases (id INTEGER PRIMARY KEY, customer_id INT REFERENCES customers(id), item TEXT);
INSERT INTO purchases (customer_id, item) VALUES (10, 'Lamp'), (20, 'Desk'), (NULL, 'Gift card')`)
  await sidebar().getByTestId('sidebar-purchases').click()
  const view = page.getByTestId('data-purchases')
  const header = view.getByRole('columnheader', { name: /customer_id/ })
  await expect(header.getByLabel('foreign key')).toBeVisible()
  await expect(header).toHaveAttribute('title', /References customers\.id/)

  // Double-click opens a searchable list of customers (id + name).
  const fkCell = view.locator('[data-row="0"] [data-col="1"]')
  await fkCell.dblclick()
  const pick = page.getByRole('dialog', { name: 'Choose customers.id' })
  await expect(pick.getByRole('option').filter({ hasText: /^10Customer 10$/ }).getByLabel('current')).toBeVisible()
  await pick.getByLabel('Search referenced rows').fill('Customer 33')
  await expect(pick.getByRole('option').first()).toContainText('Customer 33')
  await page.keyboard.press('Enter')
  await expect(pick).toHaveCount(0)
  await expect(fkCell).toHaveText('33')
  // Closing the window with this uncommitted edit doesn't ask: it's discarded.
  expect(await page.evaluate(() => {
    const e = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(e)
    return e.defaultPrevented
  })).toBe(false)
  // "Edit as text" falls back to the plain editor; typing also edits as text.
  await fkCell.dblclick()
  await page.getByRole('dialog', { name: 'Choose customers.id' }).getByRole('button', { name: 'Edit as text' }).click()
  await expect(view.getByLabel('Edit cell')).toHaveValue('33')
  await page.keyboard.press('Escape')
  await view.getByRole('button', { name: 'Discard' }).click()
  await expect(fkCell).toHaveText('10')

  // The arrow (shown on hover/selection) opens customers filtered to id 10.
  await fkCell.hover()
  await view.getByRole('button', { name: 'Open customers.id = 10' }).click()
  const customers = page.getByTestId('data-customers')
  await expect(page.getByRole('tab', { name: /customers/ })).toHaveAttribute('aria-selected', 'true')
  await expect(customers.getByText('1–1 of 1 rows')).toBeVisible()
  await expect(customers.locator('[data-row="0"] [data-col="0"]')).toHaveText('10')
  await expect(customers.getByLabel('Filter value').first()).toHaveValue('10')

  // Following another row reuses the open tab with the new filter; NULLs have no arrow.
  await page.getByRole('tab', { name: /purchases/ }).click()
  await expect(view.locator('[data-row="2"] [data-col="1"]').getByRole('button')).toHaveCount(0)
  await view.locator('[data-row="1"] [data-col="1"]').click({ button: 'right' })
  await page.getByRole('menuitem', { name: /Open customers where id = 20/ }).click()
  await expect(customers.locator('[data-row="0"] [data-col="0"]')).toHaveText('20')
  await expect(page.getByRole('tab', { name: /customers/ })).toHaveCount(1)

  await page.keyboard.press('Control+w')
  await page.getByRole('tab', { name: /purchases/ }).click()
  await page.keyboard.press('Control+w')
  await runSQL('DROP TABLE purchases')
})

test('multi-column foreign keys: follow and pick both columns', async () => {
  await runSQL(`CREATE TABLE stock (shop INT, sku TEXT, title TEXT, PRIMARY KEY (shop, sku));
INSERT INTO stock VALUES (1, 'A1', 'Apples'), (1, 'B2', 'Bread'), (2, 'A1', 'Avocado');
CREATE TABLE picks (id INTEGER PRIMARY KEY, shop INT, sku TEXT, FOREIGN KEY (shop, sku) REFERENCES stock (shop, sku));
INSERT INTO picks (shop, sku) VALUES (1, 'B2'), (2, 'A1')`)
  await sidebar().getByTestId('sidebar-picks').click()
  const view = page.getByTestId('data-picks')
  await expect(view.getByRole('columnheader', { name: /shop/ })).toHaveAttribute('title', /References stock\(shop, sku\)/)

  // Following uses both columns.
  const skuCell = view.locator('[data-row="1"] [data-col="2"]')
  await skuCell.hover()
  await view.getByRole('button', { name: 'Open stock(shop, sku)' }).click()
  const stock = page.getByTestId('data-stock')
  await expect(stock.getByText('1–1 of 1 rows')).toBeVisible()
  await expect(stock.locator('[data-row="0"] [data-col="2"]')).toHaveText('Avocado')
  await expect(stock.getByLabel('Filter value')).toHaveCount(2)
  await page.keyboard.press('Control+w')

  // Picking sets every column of the key at once.
  await page.getByRole('tab', { name: /picks/ }).click()
  await view.locator('[data-row="0"] [data-col="1"]').dblclick()
  const pick = page.getByRole('dialog', { name: 'Choose stock(shop, sku)' })
  await expect(pick.getByRole('option')).toHaveCount(3)
  await pick.getByLabel('Search referenced rows').fill('avo')
  await expect(pick.getByRole('option')).toHaveCount(1)
  await pick.getByRole('option').click()
  await expect(view.locator('[data-row="0"] [data-col="1"]')).toHaveText('2')
  await expect(view.locator('[data-row="0"] [data-col="2"]')).toHaveText('A1')
  await view.getByRole('button', { name: 'Discard' }).click()
  // The tab drops its "uncommitted" mark just after; closing before that asks first.
  await expect(page.getByRole('tab', { name: /picks/ }).getByTitle('Uncommitted changes')).toHaveCount(0)
  await page.keyboard.press('Control+w')
  await runSQL('DROP TABLE picks; DROP TABLE stock')
})

test('sidebar: saved queries, history, collapsing panels', async () => {
  const sideTabs = page.getByRole('tablist', { name: 'Sidebar' })
  // Run a query, save it with Ctrl+S under a name.
  await runSQL("SELECT name FROM customers WHERE city = 'Berlin'")
  await expect(page.getByTestId('sql-editor').last()).toBeVisible()
  await page.keyboard.press('Control+s')
  const nameDialog = page.getByRole('dialog', { name: 'Save query' })
  await nameDialog.getByLabel('Query name').fill('Berliners')
  await nameDialog.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('tab', { name: /Berliners/ })).toHaveAttribute('aria-selected', 'true')

  // Queries tab lists it; editing marks the tab, Ctrl+S updates without asking.
  await sideTabs.getByRole('tab', { name: 'Queries' }).click()
  const saved = page.getByRole('list', { name: 'Saved queries' })
  await expect(saved.getByRole('listitem').filter({ hasText: /Berliners/ })).toBeVisible()
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await page.keyboard.press('End')
  await page.keyboard.type(' LIMIT 5')
  await expect(page.getByRole('tab', { name: /Berliners/ }).getByTitle('Edited since saved (Ctrl+S)')).toBeVisible()
  await page.keyboard.press('Control+s')
  await expect(page.getByRole('dialog', { name: 'Save query' })).toHaveCount(0)
  await expect(page.getByRole('tab', { name: /Berliners/ }).getByTitle('Edited since saved (Ctrl+S)')).toHaveCount(0)
  await expect(saved.getByRole('listitem').filter({ hasText: /Berliners/ })).toContainText('LIMIT 5')

  // Closing the tab and clicking the saved query reopens it.
  await page.keyboard.press('Control+w')
  await expect(page.getByRole('tab', { name: /Berliners/ })).toHaveCount(0)
  await saved.getByRole('listitem').filter({ hasText: /Berliners/ }).click()
  await expect(page.getByRole('tab', { name: /Berliners/ })).toHaveAttribute('aria-selected', 'true')

  // History shows what ran, including failures; clicking opens it again.
  await runSQL('SELECT * FROM no_such_table')
  await sideTabs.getByRole('tab', { name: 'History' }).click()
  const hist = page.getByRole('list', { name: 'Query history' })
  await expect(hist.getByRole('listitem').filter({ hasText: /no_such_table/ }).first().getByLabel('failed')).toBeVisible()
  await expect(hist.getByRole('listitem').filter({ hasText: /city = 'Berlin'/ }).first().getByLabel('succeeded')).toBeVisible()
  await page.getByLabel('Search history').fill('no_such')
  await expect(hist.getByRole('listitem')).toHaveCount(1)
  const before = await page.getByRole('tab').count()
  await hist.getByRole('listitem').first().click()
  await expect(page.getByRole('tab')).toHaveCount(before + 1)
  await expect(page.getByTestId('sql-editor').last()).toContainText('no_such_table')
  await page.getByLabel('Search history').fill('')

  // Ctrl+B hides the table sidebar, Ctrl+Shift+B the database rail.
  await sideTabs.getByRole('tab', { name: 'Items' }).click()
  await expect(sidebar()).toBeVisible()
  await page.keyboard.press('Control+b')
  await expect(sidebar()).toHaveCount(0)
  await page.keyboard.press('Control+Shift+b')
  await expect(page.getByRole('navigation', { name: 'Open databases' })).toHaveCount(0)
  await page.getByRole('button', { name: /Show sidebar/ }).click()
  await page.getByRole('button', { name: /Show databases/ }).click()
  await expect(sidebar()).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Open databases' })).toBeVisible()
})

test('refresh icons spin while refreshing, then stop', async () => {
  const button = page.getByRole('button', { name: 'Refresh tables' })
  const spinning = () => button.locator('svg').evaluate((el) => getComputedStyle(el).animationName)
  expect(await spinning()).toBe('none')
  await button.click()
  expect(await spinning()).toBe('spin')
  await expect(button).toHaveAttribute('aria-busy', 'true')
  // At least one full turn, then it stops on its own.
  await expect.poll(spinning, { timeout: 3000 }).toBe('none')
  await expect(button).not.toHaveAttribute('aria-busy')
})

test('Ctrl+R / F5 in the table sidebar refresh the table list', async () => {
  // A change GoTable doesn't know about: another program edits the file.
  const outside = (sql: string) => { const db = new DatabaseSync(join(process.env.GOTABLE_TMP!, 'shop.sqlite')); db.exec(sql); db.close() }
  outside('CREATE TABLE made_elsewhere (id INTEGER PRIMARY KEY)')
  await expect(sidebar().getByTestId('sidebar-made_elsewhere')).toHaveCount(0)
  const filter = page.getByLabel('Filter tables')
  await filter.click()
  await page.keyboard.press('Control+r')
  await expect(sidebar().getByTestId('sidebar-made_elsewhere')).toBeVisible()
  outside('DROP TABLE made_elsewhere')
  await sidebar().getByTestId('sidebar-customers').focus()
  await page.keyboard.press('F5')
  await expect(sidebar().getByTestId('sidebar-made_elsewhere')).toHaveCount(0)
})

test('Ctrl+Tab / Ctrl+Shift+Tab walk through the open tabs', async () => {
  const bar = page.getByRole('tablist', { name: 'Open tabs' })
  // Start from a known set: close everything, then open three tabs.
  while (await bar.getByRole('tab').count()) { await bar.getByRole('tab').first().click(); await page.keyboard.press('Control+w') }
  await sidebar().getByTestId('sidebar-customers').click()
  await sidebar().getByTestId('sidebar-berliners').click()
  await page.keyboard.press('Control+t')
  const names = ['customers', 'berliners', 'Query']
  const current = async () => (await bar.getByRole('tab', { selected: true }).textContent()) ?? ''
  await expect.poll(current).toContain('Query')

  // Forward wraps from the last tab to the first; backward wraps the other way.
  for (const want of [0, 1, 2]) { await page.keyboard.press('Control+Tab'); await expect.poll(current).toContain(names[want]) }
  await page.keyboard.press('Control+Shift+Tab')
  await expect.poll(current).toContain('berliners')
  await page.keyboard.press('Control+PageUp')
  await expect.poll(current).toContain('customers')
  await page.keyboard.press('Control+PageUp')
  await expect.poll(current).toContain('Query')
  await page.keyboard.press('Control+PageDown')
  await expect.poll(current).toContain('customers')

  // Works from inside the grid (without moving the cell) and the SQL editor.
  const view = page.getByTestId('data-customers')
  await view.locator('[data-row="0"] [data-col="1"]').click()
  await page.keyboard.press('Control+Tab')
  await expect.poll(current).toContain('berliners')
  await page.keyboard.press('Control+Shift+Tab')
  await expect.poll(current).toContain('customers')
  await expect(view.locator('[data-row="0"] [data-col="1"]')).toHaveClass(/outline-accent/)
  await page.keyboard.press('Control+Shift+Tab')
  await expect.poll(current).toContain('Query')
  await page.getByTestId('sql-editor').last().locator('.cm-content').click()
  await page.keyboard.press('Control+Tab')
  await expect.poll(current).toContain('customers')

  // The native window (WebKitGTK) sends Shift+Tab as key "Unidentified" with
  // code "Tab" (GTK's ISO_Left_Tab); that must work the same.
  const gtkShiftTab = (ctrl: boolean) => page.evaluate((ctrlKey) => {
    const target = document.activeElement ?? document.body
    target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Unidentified', code: 'Tab', ctrlKey, shiftKey: true, bubbles: true, cancelable: true }))
  }, ctrl)
  await gtkShiftTab(true)
  await expect.poll(current).toContain('Query')
  await gtkShiftTab(true)
  await expect.poll(current).toContain('berliners')
  await page.getByRole('tab', { name: /customers/ }).click()
  const cell = (c: number) => view.locator(`[data-row="0"] [data-col="${c}"]`)
  await cell(2).click()
  await gtkShiftTab(false) // moves one cell left in the grid
  await expect(cell(1)).toHaveClass(/outline-accent/)
})

test('arrow keys walk through the tables in the sidebar', async () => {
  const items = sidebar().locator('[data-item]')
  const names = await items.evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.item!))
  expect(names.length).toBeGreaterThan(3)
  const focused = () => page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.item ?? null)
  const bar = page.getByRole('tablist', { name: 'Open tabs' })
  const tabsBefore = await bar.getByRole('tab').count()

  // ↓ from the filter enters the list; ↓/↑ move without opening tabs.
  await page.getByLabel('Filter tables').click()
  await page.keyboard.press('ArrowDown')
  expect(await focused()).toBe(names[0])
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  expect(await focused()).toBe(names[2])
  await page.keyboard.press('ArrowUp')
  expect(await focused()).toBe(names[1])
  await expect(bar.getByRole('tab')).toHaveCount(tabsBefore)
  // The list itself didn't scroll the page away.
  expect(await page.evaluate(() => document.scrollingElement!.scrollTop)).toBe(0)

  // End / Home jump; ↑ at the top stays put.
  await page.keyboard.press('End')
  expect(await focused()).toBe(names.at(-1))
  await page.keyboard.press('Home')
  await page.keyboard.press('ArrowUp')
  expect(await focused()).toBe(names[0])

  // Enter opens the focused table.
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(bar.getByRole('tab', { selected: true })).toContainText(names[1])

  // Shift+↓ extends the selection (then Export / Delete apply to all of it).
  await sidebar().locator(`[data-item="${names[1]}"]`).focus()
  await page.keyboard.press('Shift+ArrowDown')
  await page.keyboard.press('Shift+ArrowDown')
  await expect(sidebar().locator('[data-item][aria-selected="true"]')).toHaveCount(3)
  await expect(page.getByRole('toolbar', { name: 'Selected tables' })).toContainText('3')
  await page.keyboard.press('Escape')
  await expect(sidebar().locator('[data-item][aria-selected="true"]')).toHaveCount(0)
})

test('arrow keys keep the focused table and cell fully in view', async () => {
  // Whether el (plus its focus ring) lies inside the visible part of the
  // scroll area, below the grid's sticky header when there is one.
  const inView = (el: HTMLElement, scroller: HTMLElement, ring: number, header: number) => {
    const r = el.getBoundingClientRect()
    const s = scroller.getBoundingClientRect()
    return r.top - ring >= s.top + header - 0.5 && r.bottom + ring <= s.top + scroller.clientHeight + 0.5
  }
  const nav = sidebar()
  await nav.locator('[data-item]').first().focus()
  for (const key of ['End', ...Array(20).fill('ArrowUp'), 'Home', ...Array(20).fill('ArrowDown')]) {
    await page.keyboard.press(key)
    expect(await nav.evaluate((n, f) => new Function('return ' + f)()(document.activeElement, n, 2, 0), inView.toString())).toBe(true)
  }
  await page.keyboard.press('Escape')

  await nav.getByRole('button', { name: 'customers', exact: true }).click()
  const grid = page.getByTestId('data-customers').getByTestId('grid')
  await grid.getByRole('gridcell').first().click()
  for (const key of [...Array(40).fill('ArrowDown'), ...Array(40).fill('ArrowUp')]) {
    await page.keyboard.press(key)
    expect(await grid.evaluate((g, f) => new Function('return ' + f)()(g.querySelector('.outline-accent'), g, 0, 30), inView.toString())).toBe(true)
  }
  await page.keyboard.press('Control+w')
})

test('Ctrl+P / Ctrl+D open on the current item without flashing the first row', async () => {
  // Record what each painted frame highlights (sampled right before paint).
  // Chrome happens to hide the old bug (it never painted the in-between
  // state); WebKitGTK, the native window's engine, painted the first row
  // for a frame. This guards the behaviour; the WebKitGTK check was manual.
  const watch = () => page.evaluate(() => {
    const w = window as unknown as { __seen: string[]; __stop?: () => void }
    w.__stop?.()
    w.__seen = []
    let on = true
    w.__stop = () => { on = false }
    const frame = () => {
      if (!on) return
      document.querySelectorAll('[role=dialog] [role=option][aria-selected=true]').forEach((o) => {
        const r = o.getBoundingClientRect()
        const box = o.closest('[role=listbox], [role=dialog]')!.getBoundingClientRect()
        const t = (o.textContent ?? '') + (r.top >= box.top && r.bottom <= box.bottom ? '' : ' (scrolled away)')
        if (w.__seen.at(-1) !== t) w.__seen.push(t)
      })
      requestAnimationFrame(frame)
    }
    requestAnimationFrame(frame)
  })
  const seen = () => page.evaluate(() => (window as unknown as { __seen: string[] }).__seen)

  // Make a table that isn't first in the list the current one.
  const names = await sidebar().locator('[data-item]').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.item!))
  const pick = names[Math.min(3, names.length - 1)]
  await sidebar().locator(`[data-item="${pick}"]`).click()
  await watch()
  await page.keyboard.press('Control+p')
  const tables = page.getByRole('dialog', { name: /^Tables in / })
  await expect(tables.getByRole('option', { selected: true })).toContainText(pick)
  await page.waitForTimeout(300)
  expect(await seen()).toEqual([expect.stringContaining(pick)])
  expect((await seen())[0]).not.toContain('scrolled away')
  await page.keyboard.press('Escape')

  // Same for databases: the open one is highlighted from the start.
  await watch()
  await page.keyboard.press('Control+d')
  const dbs = page.getByRole('dialog', { name: 'Open database' })
  await expect(dbs.getByRole('option', { selected: true })).toContainText('main')
  await page.waitForTimeout(300)
  expect(await seen()).toEqual([expect.stringContaining('main')])
  await page.keyboard.press('Escape')
})

test('creating a table and a new SQLite database file', async () => {
  await sidebar().getByTestId('sidebar-customers').click()
  const bar = page.getByRole('tablist', { name: 'Open tabs' })
  // An untouched designer closes without asking.
  await page.getByRole('button', { name: 'New table', exact: true }).click()
  await expect(bar.getByRole('tab', { selected: true })).toContainText('New table')
  await page.keyboard.press('Control+w')
  await expect(bar.getByRole('tab', { name: /New table/ })).toHaveCount(0)
  // "New table" opens as a page in the main view, not a popup.
  await page.getByRole('button', { name: 'New table', exact: true }).click()
  const dialog = page.getByTestId('create-table')
  await expect(dialog).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  const sql = dialog.getByTestId('table-sql')
  // Starts with an auto-numbered id and an empty column to fill in.
  await dialog.getByLabel('Table name').fill('customers')
  await expect(dialog.getByRole('alert')).toContainText('A table named “customers” already exists.')
  await expect(dialog.getByRole('button', { name: 'Create table' })).toBeDisabled()
  await dialog.getByLabel('Table name').fill('tasks')
  const cols = dialog.getByRole('listitem')
  await cols.nth(1).getByLabel('Column name').fill('title')
  await cols.nth(1).getByLabel('Allow NULL').uncheck()
  await cols.nth(1).getByLabel('Column name').press('Enter') // adds a row
  await expect(cols).toHaveCount(3)
  await cols.nth(2).getByLabel('Column name').fill('done')
  await cols.nth(2).getByLabel('Column type').selectOption('BOOLEAN')
  await cols.nth(2).getByLabel('Default kind').selectOption('value')
  await cols.nth(2).getByLabel('Default value').fill('0')
  await expect(sql).toHaveText(`CREATE TABLE "tasks" (\n  "id" INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,\n  "title" TEXT NOT NULL,\n  "done" BOOLEAN DEFAULT '0'\n);`)
  // SQLite only auto-numbers an INTEGER key: other types switch A_I off.
  await cols.nth(0).getByLabel('Column type').selectOption('BIGINT')
  await expect(cols.nth(0).getByLabel('Auto-increment')).not.toBeChecked()
  await expect(cols.nth(0).getByLabel('Auto-increment')).toBeDisabled()
  await expect(sql).toContainText('PRIMARY KEY ("id")')
  await cols.nth(0).getByLabel('Column type').selectOption('INTEGER')
  await cols.nth(0).getByLabel('Auto-increment').check()
  // Index, and more options: collation.
  await cols.nth(1).getByLabel('Index').selectOption('unique')
  await cols.nth(1).getByRole('button', { name: 'More options' }).click()
  await cols.nth(1).getByLabel('Collation').fill('NOCASE')
  await expect(sql).toContainText('"title" TEXT COLLATE NOCASE NOT NULL')
  await expect(sql).toContainText('CONSTRAINT "tasks_title_key" UNIQUE ("title")')
  // STRICT tables explain which types they allow.
  await dialog.getByRole('region', { name: 'Table options' }).getByRole('switch').click()
  await expect(sql).toContainText('done: a STRICT table only allows INT, INTEGER, REAL, TEXT, BLOB or ANY')
  await dialog.getByRole('region', { name: 'Table options' }).getByRole('switch').click()
  // Add several columns at once, reorder, and drop the spare ones.
  await dialog.getByLabel('How many columns to add').fill('2')
  await dialog.getByRole('button', { name: 'columns', exact: true }).click()
  await expect(cols).toHaveCount(5)
  await cols.nth(4).getByRole('button', { name: 'Remove column' }).click()
  await cols.nth(3).getByRole('button', { name: 'Remove column' }).click()
  await cols.nth(1).getByRole('button', { name: 'Move down' }).click()
  await expect(cols.nth(2).getByLabel('Column name')).toHaveValue('title')
  await cols.nth(2).getByRole('button', { name: 'Move up' }).click()
  await expect(sql).toHaveText(`CREATE TABLE "tasks" (\n  "id" INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,\n  "title" TEXT COLLATE NOCASE NOT NULL,\n  "done" BOOLEAN DEFAULT '0',\n  CONSTRAINT "tasks_title_key" UNIQUE ("title")\n);`)
  // A started design isn't lost by accident.
  await page.keyboard.press('Control+w')
  const ask = page.getByRole('dialog', { name: 'Discard the new table?' })
  await expect(ask).toBeVisible()
  await ask.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toBeVisible()
  await expect(dialog.getByLabel('Table name')).toHaveValue('tasks')
  // Ctrl+Enter creates it; the table's tab takes the designer's place.
  await expect(sql).toContainText('CREATE TABLE "tasks"')
  await page.keyboard.press('Control+Enter')
  await expect(dialog).toHaveCount(0)
  await expect(bar.getByRole('tab', { name: /New table/ })).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: 'Table “tasks” created' })).toBeVisible()
  await expect(sidebar().getByTestId('sidebar-tasks')).toBeVisible()
  await expect(page.getByRole('tablist', { name: 'Open tabs' }).getByRole('tab', { selected: true })).toContainText('tasks')
  await runSQL("INSERT INTO tasks (title) VALUES ('a'), ('b'); SELECT id, done FROM tasks ORDER BY id")
  await expect(grid().last().locator('[data-row="1"] [data-col="0"]')).toHaveText('2')
  await runSQL('DROP TABLE tasks')

  // SQLite: a new database is a new file, saved as a connection and opened.
  await page.keyboard.press('Control+d')
  await page.getByRole('dialog', { name: 'Open database' }).getByRole('option', { name: /Create database file/ }).click()
  const newDb = page.getByRole('dialog', { name: 'New database file' })
  await newDb.getByLabel('Database name').fill('inventory')
  await expect(newDb.getByLabel('Database file')).toHaveAttribute('placeholder', join(process.env.GOTABLE_TMP!, 'inventory.sqlite'))
  await newDb.getByRole('button', { name: 'Create and open' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Database “inventory” created' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^SQLite inventory/ })).toBeVisible() // the new connection is open
  await expect(sidebar().getByText('This database has no tables yet.')).toBeVisible()
  await expect(sidebar().getByRole('button', { name: 'Create a table' })).toBeVisible()
  // Back to the shop connection for the remaining tests.
  await page.getByRole('button', { name: 'Disconnect' }).click()
  await page.getByRole('option', { name: /shop/ }).dblclick()
  await expect(sidebar().getByTestId('sidebar-customers')).toBeVisible()
})

test('server info lives in the top bar; there is no status bar', async () => {
  const info = page.getByTestId('server-info').locator('visible=true')
  await expect(info).toContainText(/SQLite \d+\.\d+/)
  await expect(info).toHaveAttribute('title', /Database: main/)
  await expect(page.locator('footer:visible')).toHaveCount(0)
})

test('clickable things show a pointer cursor', async () => {
  const cursor = (l: ReturnType<Page['locator']>) => l.first().evaluate((el) => getComputedStyle(el).cursor)
  await sidebar().getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  expect(await cursor(sidebar().getByRole('button', { name: 'customers', exact: true }))).toBe('pointer')
  expect(await cursor(page.getByRole('tab', { name: /customers/ }))).toBe('pointer')
  expect(await cursor(page.getByRole('button', { name: 'Refresh tables' }))).toBe('pointer')
  expect(await cursor(view.getByRole('columnheader', { name: /name/ }))).toBe('pointer')
  expect(await cursor(view.getByRole('tab', { name: 'Structure' }))).toBe('pointer')
  expect(await cursor(page.getByRole('button', { name: 'Open database' }))).toBe('pointer')
  // Disabled controls say so; data and text keep their normal cursors.
  expect(await cursor(view.getByRole('button', { name: 'Previous page' }))).toBe('not-allowed')
  expect(await cursor(view.getByRole('gridcell'))).toBe('auto')
  expect(await cursor(view.getByLabel('Quick search'))).toBe('text')
})

test('theme toggle and returning to the connection list', async () => {
  await page.getByRole('button', { name: /Dark mode|Light mode/ }).first().click()
  const theme = await page.evaluate(() => document.documentElement.dataset.theme)
  expect(['light', 'dark']).toContain(theme)
  await page.getByRole('button', { name: 'Connections' }).click()
  await expect(page.getByRole('option', { name: /shop/ })).toBeVisible()
  // The session stays open: double-click switches back instantly.
  await page.getByRole('option', { name: /shop/ }).dblclick()
  await expect(page.getByTestId('data-customers')).toBeAttached()
})

test('a connection that takes a while: progress shown, opened only once', async () => {
  await page.getByRole('button', { name: 'Connections' }).click()
  await page.keyboard.press('Control+n')
  await form().getByRole('radio', { name: 'SQLite' }).click()
  await form().getByPlaceholder('/home/you/data/app.sqlite').fill(join(process.env.GOTABLE_TMP!, 'slow.sqlite'))
  await form().getByRole('button', { name: 'Save', exact: true }).click()
  const item = page.getByRole('option', { name: /slow/ })
  await expect(item).toBeVisible()

  // Opening the session is slow (like an SSH tunnel); clicks meanwhile do nothing.
  let opens = 0
  let release!: () => void
  const held = new Promise<void>((ok) => { release = ok })
  await page.route('**/api/sessions', async (route) => {
    if (route.request().method() === 'POST') { opens++; await held }
    await route.fallback()
  })
  await item.dblclick()
  await expect(item).toHaveAttribute('aria-busy', 'true')
  await expect(item).toContainText('Connecting…')
  await expect(form().getByRole('button', { name: 'Connecting…' })).toBeDisabled()
  await item.dblclick()
  await item.dblclick()
  release()
  await expect(page.getByRole('navigation', { name: 'Tables' }).last()).toBeVisible()
  expect(opens).toBe(1)
  await page.unroute('**/api/sessions')
  await page.getByRole('button', { name: 'Disconnect' }).click()
  // Back to the shop workspace, where the next test starts.
  await page.getByRole('option', { name: /shop/ }).dblclick()
  await expect(page.getByTestId('data-customers')).toBeAttached()
})

test('PostgreSQL via a pasted URL (needs GOTABLE_E2E_PG)', async () => {
  test.skip(!process.env.GOTABLE_E2E_PG, 'set GOTABLE_E2E_PG to a postgres:// URL with the demo data')
  await page.getByRole('button', { name: 'Connections' }).click()
  await page.keyboard.press('Control+n')
  await form().getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(process.env.GOTABLE_E2E_PG!)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form().getByRole('radio', { name: 'Production' }).click()
  await form().getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByText('changes you commit are real')).toBeVisible()
  // Ctrl+D lists schemas and the server's databases; another database
  // opens next to this one (its own connection) and closes again.
  await runSQL('DROP DATABASE IF EXISTS gt_e2e_other; CREATE DATABASE gt_e2e_other')
  await page.keyboard.press('Control+d')
  const dbs = page.getByRole('dialog', { name: 'Open database' })
  await expect(dbs.getByText('Databases on this server')).toBeVisible()
  await expect(dbs.getByRole('option', { name: /^public/ })).toBeVisible()
  await dbs.getByLabel('Search commands').fill('gt_e2e_other')
  await page.keyboard.press('Enter')
  const rail = page.getByRole('navigation', { name: 'Open databases' })
  const other = rail.getByRole('button', { name: 'Database gt_e2e_other › public' })
  await expect(other).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('db-empty')).toContainText('gt_e2e_other › public')
  await other.hover()
  await rail.getByRole('button', { name: 'Close gt_e2e_other › public' }).click()
  await expect(other).toHaveCount(0)
  await runSQL('DROP DATABASE gt_e2e_other WITH (FORCE)')

  // Creating a database and a schema from the Ctrl+D list opens them.
  for (const [kind, name, label] of [['Database', 'gt_e2e_made', 'gt_e2e_made › public'], ['Schema in this database', 'gt_e2e_schema', 'postgres › gt_e2e_schema']] as const) {
    await page.keyboard.press('Control+d')
    await page.getByRole('dialog', { name: 'Open database' }).getByRole('option', { name: /Create database or schema/ }).click()
    const made = page.getByRole('dialog', { name: 'New database or schema' })
    await made.getByRole('radio', { name: kind, exact: true }).click()
    await made.getByLabel('Database name').fill(name)
    await expect(made.getByTestId('database-sql')).toHaveText(kind === 'Database' ? `CREATE DATABASE "${name}";` : `CREATE SCHEMA "${name}";`)
    await made.getByRole('button', { name: /^Create / }).click()
    const tile = page.getByRole('navigation', { name: 'Open databases' }).getByRole('button', { name: `Database ${label}` })
    await expect(tile).toHaveAttribute('aria-pressed', 'true')
    await tile.hover()
    await page.getByRole('navigation', { name: 'Open databases' }).getByRole('button', { name: `Close ${label}` }).click()
  }
  await runSQL('DROP DATABASE gt_e2e_made WITH (FORCE); DROP SCHEMA gt_e2e_schema')
  await sidebar().getByRole('button', { name: 'orders' }).click()
  const view = page.getByTestId('data-orders')
  await expect(view.getByText('1–300 of 1,500 rows')).toBeVisible()
  await view.getByRole('tab', { name: 'Structure' }).click()
  await expect(view.getByText('orders_customer_idx')).toBeVisible()
})
