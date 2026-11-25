import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// The ER diagram, documents as JSON and trees, processes and users. The
// diagram runs on a SQLite file; the others need a server, as in CI.

async function open(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => { if (m.type() === 'error' && !m.text().startsWith('Failed to load resource')) errors.push(m.text()) })
  await page.goto(process.env.GOTABLE_URL!)
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  return errors
}

async function connectURL(page: Page, url: string) {
  const errors = await open(page)
  const form = page.getByLabel('Connection form')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await page.getByTestId('server-info').locator('visible=true').waitFor()
  return errors
}

const tool = (page: Page, name: string) => page.getByRole('button', { name, exact: true }).locator('visible=true').click()

const runSQL = async (page: Page, sql: string, wait = true) => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(sql)
  await page.keyboard.press('Control+Shift+Enter')
  if (wait) await expect(page.getByRole('button', { name: /^Stop ·/ })).toHaveCount(0)
}

test('ER diagram: tables and keys, drawn, arranged and changed', async ({ page }) => {
  const file = join(process.env.GOTABLE_TMP!, 'diagram.sqlite')
  const db = new DatabaseSync(file)
  db.exec(`CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT NOT NULL, boss INTEGER REFERENCES people(id));
    CREATE TABLE orders (id INTEGER PRIMARY KEY, person INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE, total REAL);
    CREATE TABLE notes (id INTEGER PRIMARY KEY, order_id INTEGER, body TEXT);`)
  db.close()
  const errors = await open(page)
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(file)
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByText('Jump anywhere')).toBeVisible()
  await tool(page, 'ER diagram')
  const d = page.getByTestId('diagram')
  for (const t of ['people', 'orders', 'notes']) await expect(d.getByTestId(`er-${t}`)).toBeVisible()
  const world = d.getByTestId('er-world')
  await expect(world).toHaveAttribute('data-edges', '2')
  // What is referred to stands left of what refers to it.
  const box = async (t: string) => (await d.getByTestId(`er-${t}`).boundingBox())!
  expect((await box('people')).x).toBeLessThan((await box('orders')).x)

  // A key drawn from one column to another; nothing else moves.
  const before = await box('people')
  await d.getByTestId('er-notes-order_id').hover()
  const from = (await d.getByTestId('er-notes-order_id').boundingBox())!
  const to = (await d.getByTestId('er-orders-id').boundingBox())!
  await page.mouse.move(from.x + from.width, from.y + from.height / 2) // its dot
  await page.mouse.down()
  await page.mouse.move(to.x + 30, to.y + to.height / 2, { steps: 6 })
  await page.mouse.up()
  const dlg = page.getByRole('dialog', { name: 'Add foreign key' })
  await expect(dlg.getByLabel('Referenced table')).toHaveValue('orders')
  await expect(dlg.getByLabel('Column 1', { exact: true })).toHaveValue('order_id')
  await expect(dlg.getByTestId('alter-sql')).toContainText('REFERENCES')
  await dlg.getByRole('button', { name: 'Add foreign key' }).click()
  await expect(world).toHaveAttribute('data-edges', '3')
  expect((await box('people')).x).toBe(before.x)

  // Columns are changed from the diagram too.
  await d.getByTestId('er-notes').getByText('notes', { exact: true }).click({ button: 'right' })
  await page.getByRole('menuitem', { name: 'Add column…' }).click()
  const add = page.getByRole('dialog', { name: /Add column/ })
  await add.getByLabel('Column name').fill('author')
  await add.getByRole('button', { name: 'Add column' }).click()
  await expect(d.getByTestId('er-notes-author')).toBeVisible()

  // A table is dragged where it suits, and stays there after a reload.
  await expect(page.getByText('Column “author” added')).toHaveCount(0, { timeout: 10_000 })
  const was = await box('notes')
  await page.mouse.move(was.x + 50, was.y + 12)
  await page.mouse.down()
  await page.mouse.move(was.x + 110, was.y - 48, { steps: 6 })
  await page.mouse.up()
  const now = await box('notes')
  expect(Math.round(now.x - was.x)).toBeGreaterThan(40)
  expect(Math.round(was.y - now.y)).toBeGreaterThan(40)
  await d.getByRole('button', { name: 'Refresh (Ctrl+R)' }).click()
  await expect(d).toHaveAttribute('aria-busy', 'false')
  expect((await box('notes')).x).toBe(now.x)

  await d.getByLabel('Find in the diagram').fill('total')
  await expect(d.getByText('1 found')).toBeVisible()
  await d.getByLabel('Find in the diagram').fill('')
  await d.getByLabel('Columns shown').selectOption('keys')
  await expect(d.getByTestId('er-people-name')).toHaveCount(0)
  await expect(d.getByTestId('er-people-boss')).toBeVisible()
  await d.getByLabel('Columns shown').selectOption('all')
  // Double-clicking a table opens its rows.
  await d.getByTestId('er-orders').getByText('orders', { exact: true }).dblclick()
  await expect(page.getByTestId('data-orders')).toBeVisible()
  expect(errors).toEqual([])
})

test('documents as JSON and as a tree: MongoDB (needs GOTABLE_E2E_MONGODB)', async ({ page }) => {
  const url = process.env.GOTABLE_E2E_MONGODB ?? ''
  test.skip(!url, 'set GOTABLE_E2E_MONGODB')
  const errors = await connectURL(page, url)
  await runSQL(page, 'db.gt_docs.drop()\ndb.gt_docs.insertMany([{ _id: "a", n: NumberLong("9007199254740993"), age: NumberInt(31), in: { x: [NumberLong(1), "two"] }, gone: 1 }, { _id: "b", v: true }])')
  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByTestId('sidebar-gt_docs').click()
  const v = page.getByTestId('data-gt_docs')
  await v.getByRole('button', { name: 'JSON' }).click()
  const a = v.getByTestId('json-doc').filter({ hasText: '"a"' })
  await expect(a).toContainText('9007199254740993')
  await a.getByRole('button', { name: 'Edit' }).click()
  const editor = a.locator('.cm-content')
  await editor.fill('{"_id": "a", "n": 9007199254740993, "age": 32, "in": {"x": [1, "two"]},}')
  await expect(a.getByRole('alert')).toContainText('a comma too many')
  await expect(a.getByRole('button', { name: 'Apply' })).toBeDisabled()
  await editor.fill('{"_id": "a", "n": 9007199254740993, "age": 32, "in": {"x": [1, "two"]}, "new": {"k": null}}')
  await page.keyboard.press('Control+Enter')
  await expect(v.getByText('1 unsaved change')).toBeVisible()

  // The tree shows the same edits, and adds its own.
  await v.getByRole('button', { name: 'Tree' }).click()
  await expect(v.getByText('1 unsaved change')).toBeVisible()
  const row = (path: string) => v.getByTestId('tree-row').and(v.locator(`[data-path="${path}"]`))
  await row('v').getByText('true').dblclick()
  await v.getByLabel('Value').selectOption('false')
  await row('age').getByText('32').dblclick()
  await v.getByLabel('Value').fill('old')
  await page.keyboard.press('Enter')
  await expect(v.getByRole('alert')).toContainText('isn’t a number')
  await v.getByLabel('Value').fill('33')
  await page.keyboard.press('Enter')
  await row('in').getByRole('button', { name: 'Expand' }).click()
  await row('in.x').getByRole('button', { name: 'Expand' }).click()
  await row('in.x').hover()
  await row('in.x').getByRole('button', { name: 'Add an item' }).click()
  await v.getByLabel('Value').fill('three')
  await page.keyboard.press('Enter')
  await expect(v.getByText('2 unsaved changes')).toBeVisible()
  await v.getByRole('button', { name: /Review & commit/ }).click()
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review).toContainText('$unset')
  await expect(review).not.toContainText('9007199254740993') // untouched, so not written
  await review.getByRole('button', { name: 'Commit', exact: true }).click()
  await expect(page.getByText('Saved 2 changes')).toBeVisible()

  // Types stayed: the long in the list is a long still.
  await runSQL(page, 'db.gt_docs.aggregate([{ $match: { _id: "a" } }, { $project: { _id: 0, n: { $type: "$n" }, x: { $type: { $arrayElemAt: ["$in.x", 0] } }, age: { $type: "$age" }, gone: { $type: "$gone" } } }])')
  const cells = page.getByTestId('grid').last().getByRole('gridcell')
  for (const t of ['long', 'int', 'missing']) await expect(cells.filter({ hasText: new RegExp(`^${t}$`) }).first()).toBeVisible()
  await runSQL(page, 'db.gt_docs.drop()')
  expect(errors).toEqual([])
})

test('documents as JSON: Redis (needs GOTABLE_E2E_REDIS)', async ({ page }) => {
  const url = process.env.GOTABLE_E2E_REDIS ?? ''
  test.skip(!url, 'set GOTABLE_E2E_REDIS')
  const errors = await connectURL(page, url)
  await runSQL(page, 'DEL gtdoc:h gtdoc:new\nHSET gtdoc:h name Ann city Berlin\nEXPIRE gtdoc:h 5000')
  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByTestId('sidebar-*').click()
  const v = page.getByTestId('data-*')
  await v.getByLabel('Quick search').fill('gtdoc')
  await page.keyboard.press('Enter')
  await v.getByRole('button', { name: 'JSON' }).click()
  const h = v.getByTestId('json-doc').filter({ hasText: 'gtdoc:h' })
  await h.getByRole('button', { name: 'Edit' }).click()
  await h.locator('.cm-content').fill('{"name": "Ann", "age": "31"}')
  await page.keyboard.press('Control+Enter')
  await v.getByRole('button', { name: 'Key', exact: true }).click()
  await v.getByTestId('json-doc').last().locator('.cm-content').fill('{"key": "gtdoc:new", "type": "list", "ttl": 100, "value": ["a", "b"]}')
  await page.keyboard.press('Control+Enter')
  await v.getByRole('button', { name: /Review & commit/ }).click()
  const review = page.getByRole('dialog', { name: 'Review changes' })
  for (const cmd of ['HSET gtdoc:h age 31', 'HDEL gtdoc:h city', 'RPUSH gtdoc:new a b', 'EXPIRE gtdoc:new 100']) await expect(review).toContainText(cmd)
  await review.getByRole('button', { name: 'Commit', exact: true }).click()
  await expect(h).toContainText('"31"')
  await expect(h).toContainText('expires in')
  await runSQL(page, 'DEL gtdoc:h gtdoc:new')
  expect(errors).toEqual([])
})

test('processes and users: PostgreSQL (needs GOTABLE_E2E_PG)', async ({ page }) => {
  const url = process.env.GOTABLE_E2E_PG ?? ''
  test.skip(!url, 'set GOTABLE_E2E_PG')
  const errors = await connectURL(page, url)
  await runSQL(page, 'CREATE TABLE IF NOT EXISTS gt_tools (id serial primary key)')
  // A slow query is found while it runs, from another connection, and stopped.
  await runSQL(page, 'SELECT pg_sleep(90), 4711 AS gt_marker', false)
  await tool(page, 'Processes')
  const p = page.getByTestId('processes')
  const row = p.getByTestId('process').filter({ hasText: 'gt_marker' })
  await expect(row).toBeVisible({ timeout: 15_000 })
  await row.click()
  await expect(p.getByTestId('process-detail')).toContainText('pg_sleep')
  await p.getByTestId('process-detail').getByRole('button', { name: 'Stop query' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Stop query' }).click()
  await expect(row).toHaveCount(0, { timeout: 15_000 })

  // A user: made, given access, changed, deleted. The password never shows.
  await tool(page, 'Users & privileges')
  const u = page.getByTestId('users')
  await u.getByRole('button', { name: 'New user' }).click()
  const create = page.getByRole('dialog', { name: 'New user' })
  await create.getByLabel('User name').fill('gt_e2e_user')
  await create.getByRole('button', { name: 'Generate' }).click()
  const password = await create.getByLabel('Password', { exact: true }).inputValue()
  expect(password.length).toBeGreaterThanOrEqual(16)
  await expect(create.getByTestId('user-sql')).toContainText('••••••••')
  await expect(create.getByTestId('user-sql')).not.toContainText(password)
  await create.getByRole('button', { name: 'Create' }).click()
  await expect(u.getByRole('option', { name: /gt_e2e_user/ })).toHaveAttribute('aria-selected', 'true')
  await u.getByRole('button', { name: 'Give access' }).click()
  const give = page.getByRole('dialog', { name: 'Give access' })
  await give.getByLabel('Level').selectOption('table')
  await give.getByLabel('Table', { exact: true }).selectOption('gt_tools')
  await give.getByRole('button', { name: 'Read only' }).click()
  await expect(give.getByTestId('user-sql')).toContainText('GRANT SELECT ON TABLE')
  await give.getByRole('button', { name: 'Give access' }).click()
  await expect(u.getByTestId('grant').filter({ hasText: 'public.gt_tools' })).toContainText('SELECT')
  await u.getByRole('button', { name: 'Change access to public.gt_tools' }).click()
  const change = page.getByRole('dialog', { name: 'Access to public.gt_tools' })
  await change.getByRole('button', { name: 'INSERT', exact: true }).click()
  await change.getByRole('button', { name: 'Change' }).click()
  await expect(u.getByTestId('grant').filter({ hasText: 'public.gt_tools' })).toContainText('INSERT')
  await u.getByRole('button', { name: 'Delete' }).click()
  const drop = page.getByRole('dialog', { name: /Delete gt_e2e_user/ })
  await drop.getByRole('switch').click()
  await expect(drop.getByTestId('user-sql')).toContainText('REASSIGN OWNED')
  await drop.getByRole('button', { name: 'Delete' }).click()
  await expect(u.getByRole('option', { name: /gt_e2e_user/ })).toHaveCount(0)
  // Several at once: chosen with Ctrl and Shift, or all that were found.
  await runSQL(page, 'DROP ROLE IF EXISTS gt_bulk_1, gt_bulk_2, gt_bulk_3, gt_bulk_4; CREATE ROLE gt_bulk_1 LOGIN; CREATE ROLE gt_bulk_2 LOGIN; CREATE ROLE gt_bulk_3 LOGIN; CREATE ROLE gt_bulk_4 LOGIN')
  await tool(page, 'Users & privileges')
  await u.getByRole('button', { name: 'Refresh users' }).click()
  const list = u.getByRole('listbox', { name: 'Users' })
  const user = (n: number) => list.getByRole('option', { name: new RegExp(`gt_bulk_${n}`) })
  await user(1).click()
  await user(3).click({ modifiers: ['Shift'] })
  const bulk = u.getByTestId('users-bulk')
  await expect(bulk).toContainText('3 users selected')
  await user(2).click({ modifiers: ['Control'] })
  await expect(bulk).toContainText('2 users selected')
  await page.keyboard.press('Escape')
  await expect(bulk).toHaveCount(0)
  await u.getByLabel('Find a user').fill('gt_bulk')
  await u.getByLabel('Select all users').check()
  await expect(bulk).toContainText('4 users selected')
  await bulk.getByRole('button', { name: 'Give access…' }).click()
  await give.getByRole('button', { name: 'Read only' }).click()
  await expect(give.getByTestId('user-sql')).toContainText('gt_bulk_4')
  await give.getByRole('button', { name: 'Give access' }).click()
  await expect(page.getByText('Access given').first()).toBeVisible()
  await bulk.getByRole('button', { name: 'Delete 4…' }).click()
  const dropAll = page.getByRole('dialog', { name: 'Delete 4 users?' })
  await dropAll.getByRole('switch').click()
  await expect(dropAll.getByTestId('user-sql')).toContainText('gt_bulk_4')
  await expect(dropAll.getByRole('button', { name: 'Delete 4 users' })).toBeDisabled()
  await dropAll.getByLabel('Type DELETE to confirm').fill('delete')
  await dropAll.getByRole('button', { name: 'Delete 4 users' }).click()
  await expect(list.getByRole('option', { name: /gt_bulk/ })).toHaveCount(0)
  // The notification stands in the corner, above what is at the bottom.
  const toast = (await page.getByTestId('toasts').getByText('Deleted 4 users').boundingBox())!
  expect(toast.y).toBeGreaterThan(900 - 90)
  // Nor is it in the history.
  await page.getByRole('tab', { name: 'History' }).click()
  await expect(page.getByText('CREATE ROLE').first()).toBeVisible()
  expect(await page.locator('aside').first().textContent()).not.toContain(password)
  await page.getByRole('tab', { name: 'Items' }).click()
  await runSQL(page, 'DROP TABLE gt_tools')
  expect(errors).toEqual([])
})
