import { expect, test } from '@playwright/test'

// MongoDB through the UI: everything that differs from an SQL database. Set
// GOTABLE_E2E_MONGODB to a URL, e.g.
//   mongodb://gotable:pw@127.0.0.1:27017/gt_e2e?authSource=admin
const url = process.env.GOTABLE_E2E_MONGODB ?? ''

test('MongoDB: connect, query, browse, edit, structure (needs GOTABLE_E2E_MONGODB)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_MONGODB')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)

  const form = page.getByLabel('Connection form')
  await page.keyboard.press('Control+n')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await expect(form.getByRole('radio', { name: 'MongoDB' })).toHaveAttribute('aria-checked', 'true')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/MongoDB \d/)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('MongoDB')

  const run = async (text: string) => {
    await page.keyboard.press('Control+t')
    const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
    await editor.click()
    await editor.fill(text)
    await page.keyboard.press('Control+Shift+Enter')
  }

  // The query editor runs shell commands, one a line.
  await run(`db.dropDatabase()
db.people.insertMany([
  { _id: 1, name: 'Alice', age: 41, active: true, seen: ISODate('2024-03-15T10:20:30Z'), address: { city: 'Berlin' } },
  { _id: 2, name: 'Bob', age: 29, active: false, tags: ['x', 'y'] },
])
db.people.createIndex({ age: 1 })
db.people.find({ age: { $gt: 30 } }, { name: 1 })`)
  await expect(page.getByRole('gridcell', { name: 'Alice', exact: true })).toBeVisible()
  // SQL gets a useful answer rather than a syntax error.
  await page.keyboard.press('Control+w')
  await run('SELECT * FROM people')
  await expect(page.getByText(/runs shell commands here, not SQL/).first()).toBeVisible()
  await page.keyboard.press('Control+w')

  // A result of find() can be edited in place; with a projection it can't.
  await run('db.people.find({}).sort({ _id: 1 })')
  await expect(page.getByRole('gridcell', { name: 'Bob', exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')

  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'people', exact: true }).click()
  const view = page.getByTestId('data-people')
  const cell = (name: string) => view.getByRole('gridcell', { name, exact: true })
  await expect(cell('Alice')).toBeVisible()
  // Nested values read as JSON.
  await expect(cell('{"city":"Berlin"}')).toBeVisible()

  // Edit: the number stays a number and the review shows the real command.
  await cell('41').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('42')
  await page.keyboard.press('Enter')
  await expect(cell('42')).toBeVisible()
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText(/updateOne/)).toBeVisible()
  await expect(review.getByText(/replica set/)).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Saved 1 change' })).toBeVisible()

  // Text that isn't a number is refused, not stored as a string.
  await cell('29').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('old')
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  await expect(page.getByText(/isn.t a number/).first()).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).first().click()

  // The raw filter is a query document.
  await view.getByRole('button', { name: /^Filter/ }).first().click()
  const raw = view.getByRole('textbox', { name: 'Raw WHERE' })
  await expect(view.getByText('Query filter (optional)')).toBeVisible()
  await raw.fill('{ age: { $lt: 30 } }')
  await raw.press('Enter')
  await expect(cell('Bob')).toBeVisible()
  await expect(cell('Alice')).toBeHidden()
  await view.getByRole('button', { name: 'Clear' }).click()
  await expect(cell('Alice')).toBeVisible()

  // Structure: fields come from the documents; there's no column designer.
  await view.getByRole('tab', { name: 'Structure' }).click()
  const structure = view.getByTestId('structure')
  await expect(structure.getByRole('region', { name: 'Fields' })).toBeVisible()
  await expect(structure.getByRole('button', { name: 'Add column' })).toBeHidden()
  await expect(structure.getByRole('button', { name: 'Drop _id', exact: true })).toBeDisabled()
  await expect(structure.getByTestId('index-age_1')).toBeVisible()
  await expect(structure.getByTestId('column-age').getByText('int', { exact: true })).toBeVisible()

  // Rename a field: shows the command, and every document changes.
  await structure.getByRole('button', { name: 'Edit tags' }).click()
  const rename = page.getByRole('dialog', { name: 'Rename field tags' })
  await rename.getByRole('textbox', { name: 'New field name' }).fill('labels')
  await expect(rename.getByTestId('alter-sql')).toContainText('$rename')
  await rename.getByRole('button', { name: 'Rename', exact: true }).click()
  await expect(structure.getByTestId('column-labels')).toBeVisible()

  // Add a unique index from a field's menu, then drop it.
  await structure.getByRole('button', { name: 'More for name' }).click()
  await page.getByRole('menuitem', { name: 'Unique' }).click()
  const add = page.getByRole('dialog', { name: /unique index/ })
  await expect(add.getByText(/createIndex/)).toBeVisible()
  await add.getByRole('button', { name: /apply|run/i }).click()
  await expect(structure.getByTestId('index-name_1')).toContainText('UNIQUE')
  await structure.getByRole('button', { name: 'Drop name_1' }).click()
  const drop = page.getByRole('dialog', { name: /Drop index/ })
  await expect(drop.getByText(/dropIndex/)).toBeVisible()
  await drop.getByRole('button', { name: /drop|apply|run/i }).last().click()
  await expect(structure.getByTestId('index-name_1')).toBeHidden()
  await view.getByRole('tab', { name: 'Data' }).click()

  // The definition is shell commands.
  await view.getByRole('tab', { name: 'Definition' }).click()
  await expect(view.getByText(/db\.createCollection\("people"\)/)).toBeVisible()
  await view.getByRole('tab', { name: 'Data' }).click()

  // A new collection needs only a name.
  await page.getByRole('button', { name: 'New collection' }).click()
  const dialog = page.getByRole('dialog', { name: 'New collection' })
  await dialog.getByRole('textbox', { name: 'Collection name' }).fill('orders')
  await expect(dialog.getByText('db.createCollection("orders")')).toBeVisible()
  await dialog.getByRole('button', { name: 'Create' }).click()
  const orders = page.getByTestId('data-orders')
  await expect(orders).toBeVisible()

  // Documents bring their fields, so the first one is written as a command.
  await expect(orders.getByText('This collection is empty')).toBeVisible()
  await orders.getByRole('button', { name: 'Add a document' }).click()
  const started = page.getByTestId('sql-editor').last().locator('.cm-content')
  await expect(started).toContainText('db.orders.insertOne')
  await started.fill('db.orders.insertOne({ item: "book", qty: 2 })')
  await page.keyboard.press('Control+Shift+Enter')
  await expect(page.getByText(/OK — 1 row\(s\) affected/).last()).toBeVisible()
  await page.keyboard.press('Control+w')
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'orders', exact: true }).click()
  await orders.getByRole('button', { name: /^Refresh/ }).click()
  await expect(orders.getByRole('gridcell', { name: 'book', exact: true })).toBeVisible()

  await run('db.dropDatabase()')
  expect(errors).toEqual([])
})
