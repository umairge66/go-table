import { expect, test, type Page } from '@playwright/test'

// MongoDB through the UI: everything that differs from an SQL database. Set
// GOTABLE_E2E_MONGODB to a URL, e.g.
//   mongodb://gotable:pw@127.0.0.1:27017/gt_e2e?authSource=admin
const url = process.env.GOTABLE_E2E_MONGODB ?? ''

// shots: set GOTABLE_E2E_SHOTS to a folder to keep screenshots of the views.
const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

async function connect(page: Page) {
  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.getByLabel('Search connections').waitFor()
  await page.keyboard.press('Control+n')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await expect(form.getByRole('radio', { name: 'MongoDB' })).toHaveAttribute('aria-checked', 'true')
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/MongoDB \d/)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('MongoDB')
}

/** run opens a query tab and runs every command in it. */
const runner = (page: Page) => async (text: string) => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(text)
  await page.keyboard.press('Control+Shift+Enter')
}

test('MongoDB: connect, query, browse, edit, structure (needs GOTABLE_E2E_MONGODB)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_MONGODB')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await connect(page)
  const run = runner(page)

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

  // While the rows reload after a commit, the grid takes no edits: they'd be lost.
  await expect(view).toHaveAttribute('aria-busy', 'false')
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

// What Compass offers beyond browsing and editing.
test('MongoDB: explain, index options, validation, import, schema, pipelines (needs GOTABLE_E2E_MONGODB)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_MONGODB')
  test.setTimeout(180_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await connect(page)
  const run = runner(page)
  await run(`db.dropDatabase()
db.people.insertMany([
  { _id: 1, name: 'Alice', age: 41, city: 'Berlin', loc: { type: 'Point', coordinates: [13.4, 52.5] } },
  { _id: 2, name: 'Bob', age: 29, city: 'Paris' },
  { _id: 3, name: 'Carol', age: 35, city: 'Berlin', email: 'c@example.com' },
])
db.people.createIndex({ name: 1 })`)
  await expect(page.getByText(/OK/).last()).toBeVisible()
  await page.keyboard.press('Control+w')

  // Explain: the button explains the command under the cursor.
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill('db.people.find({ name: "Bob" })')
  await page.getByRole('button', { name: 'Explain' }).click()
  const plan = page.getByTestId('plan')
  await expect(plan.getByTestId('plan-notes')).toContainText('Used index name_1')
  await expect(plan.getByTestId('stage-IXSCAN').or(plan.getByTestId('stage-EXPRESS_IXSCAN'))).toBeVisible()
  await shot(page, 'explain-index')
  await editor.fill('db.people.find({ city: "Berlin" }).sort({ age: -1 })')
  await page.getByRole('button', { name: 'Explain' }).click()
  await expect(plan.getByTestId('plan-notes')).toContainText('Collection scan')
  await expect(plan.getByTestId('plan-notes')).toContainText('Sorted in memory')
  await shot(page, 'explain-collscan')
  // The raw output is a click away.
  await page.getByRole('button', { name: 'Raw output' }).click()
  await expect(page.getByRole('columnheader', { name: 'queryPlanner' })).toBeVisible()
  await page.keyboard.press('Control+w')

  // Index options: a key per field, TTL, partial, hidden.
  await page.getByRole('button', { name: 'Refresh tables' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'people', exact: true }).click()
  const view = page.getByTestId('data-people')
  await view.getByRole('tab', { name: 'Structure' }).click()
  const structure = view.getByTestId('structure')
  await structure.getByRole('button', { name: 'Add index' }).click()
  let dlg = page.getByRole('dialog', { name: 'Add index' })
  await dlg.getByRole('group', { name: 'Index columns' }).getByRole('button', { name: 'age' }).click()
  await dlg.getByLabel('Key of age').selectOption('-1')
  await dlg.getByLabel('Partial filter').fill('{ age: { $gte: 18 } }')
  await dlg.getByRole('switch', { name: /^Hidden/ }).click()
  await expect(dlg.getByTestId('alter-sql')).toHaveText('db.people.createIndex({"age":-1}, {"partialFilterExpression":{"age":{"$gte":18}},"hidden":true});')
  await shot(page, 'index-dialog')
  await dlg.getByRole('button', { name: 'Add index' }).click()
  const ageIdx = structure.getByTestId('index-age_-1')
  await expect(ageIdx).toContainText('HIDDEN')
  await expect(ageIdx).toContainText('partial')
  await expect(ageIdx).toContainText('age ↓')
  await structure.getByRole('button', { name: 'Unhide age_-1' }).click()
  await page.getByRole('dialog', { name: /Unhide index/ }).getByRole('button', { name: 'Run' }).click()
  await expect(ageIdx).not.toContainText('HIDDEN')
  // A TTL index and a geo one.
  await structure.getByRole('button', { name: 'Add index' }).click()
  dlg = page.getByRole('dialog', { name: 'Add index' })
  await dlg.getByRole('group', { name: 'Index columns' }).getByRole('button', { name: 'loc' }).click()
  await dlg.getByLabel('Key of loc').selectOption('2dsphere')
  await dlg.getByRole('button', { name: 'Add index' }).click()
  await expect(structure.getByTestId('index-loc_2dsphere')).toContainText('loc 2dsphere')

  // Validation: rules are written in the editor and refuse bad documents.
  const validation = structure.getByRole('region', { name: 'Validation' })
  await validation.getByRole('button', { name: 'Add rules' }).click()
  await expect(validation.getByTestId('validator')).toContainText('$jsonSchema')
  await validation.getByRole('button', { name: 'Save rules' }).click()
  const review = page.getByRole('dialog', { name: 'Change the validation rules' })
  await expect(review.getByText(/collMod/)).toBeVisible()
  await review.getByRole('button', { name: 'Run' }).click()
  await expect(validation.getByRole('button', { name: 'Remove rules' })).toBeVisible()
  // The editor now shows the rules as the server keeps them.
  await expect(validation.getByTestId('validator')).toContainText('"$jsonSchema"')
  await shot(page, 'structure-validation')
  await run('db.people.insertOne({ age: 3 })')
  await expect(page.getByText(/Document failed validation/i).first()).toBeVisible()
  await page.keyboard.press('Control+w')

  // Import JSON documents: Extended JSON keeps types; a refused one is skipped and listed.
  await view.getByRole('tab', { name: 'Data' }).click()
  await view.getByRole('button', { name: 'Import' }).click()
  const imp = page.getByRole('dialog', { name: 'Import into people' })
  await imp.locator('input[type=file]').setInputFiles({
    name: 'more.json', mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify([
      { _id: 10, name: 'Dan', age: 50, joined: { $date: '2024-05-01T00:00:00Z' } },
      { _id: 1, name: 'Alice again', age: 20 },
      { _id: 11, name: 'Eve', age: 22 },
    ])),
  })
  await imp.getByRole('switch', { name: /^Skip documents/ }).click()
  await imp.getByRole('button', { name: 'Import into people' }).click()
  await expect(imp.getByTestId('import-skipped')).toContainText('2 imported, 1 refused')
  await expect(imp.getByTestId('import-skipped')).toContainText('document 2: E11000 duplicate key')
  await shot(page, 'import-json')
  await imp.getByRole('button', { name: 'Close', exact: true }).last().click()
  await expect(view.getByRole('gridcell', { name: 'Dan', exact: true })).toBeVisible()
  await run('db.people.aggregate([{ $match: { _id: 10 } }, { $project: { t: { $type: "$joined" } } }])')
  await expect(page.getByRole('gridcell', { name: 'date', exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')

  // Schema: from a sample, each field's types, presence, range and common values.
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'people', exact: true }).click()
  await view.getByRole('tab', { name: 'Schema' }).click()
  const schema = view.getByTestId('schema')
  await expect(schema.getByTestId('schema-sampled')).toContainText('Sampled 5 of about 5')
  await expect(schema.getByTestId('field-age').getByTestId('field-range')).toHaveText('from 22 to 50')
  await expect(schema.getByTestId('field-city').getByTestId('field-top')).toContainText('Berlin')
  await expect(schema.getByTestId('field-loc.coordinates[]')).toContainText('double')
  await expect(schema.getByTestId('field-email')).toContainText('in 20% of documents')
  await shot(page, 'schema')
  await view.getByRole('tab', { name: 'Data' }).click()

  // Aggregations: each stage shows what leaves it.
  await view.getByRole('tab', { name: 'Aggregations' }).click()
  const pipe = view.getByTestId('pipeline')
  const body = async (n: number, text: string) => {
    const ed = pipe.getByTestId(`stage-${n}-editor`).locator('.cm-content')
    await ed.click()
    await ed.fill(text)
  }
  await pipe.getByRole('button', { name: 'Add stage' }).first().click()
  await body(1, '{ age: { $gte: 25 } }')
  await expect(pipe.getByTestId('stage-1-output')).toContainText('4 documents')
  await pipe.getByRole('button', { name: 'Add a stage at the end' }).click()
  await pipe.getByLabel('Stage 2 operator').selectOption('$group')
  await body(2, '{ _id: "$city", n: { $sum: 1 } }')
  await expect(pipe.getByTestId('stage-2-output')).toContainText('"Berlin"')
  // The group of documents without a city: a null _id is shown, not dropped.
  await expect(pipe.getByTestId('stage-2-output')).toContainText('_id: null')
  await pipe.getByRole('button', { name: 'Add a stage at the end' }).click()
  await pipe.getByLabel('Stage 3 operator').selectOption('$sort')
  await body(3, '{ n: -1, _id: 1 }')
  const firstDoc = pipe.getByTestId('stage-3-output').locator('.rounded-md').first()
  await expect(firstDoc).toContainText('n: 2')
  // Turning a stage off lets documents through unchanged: $sort then gets people, not groups.
  await pipe.getByTestId('stage-2').getByRole('switch').click()
  await expect(pipe.getByTestId('stage-2-output')).toContainText('This stage is off')
  await expect(pipe.getByTestId('stage-3-output')).toContainText('name: "Alice"')
  await pipe.getByTestId('stage-2').getByRole('switch').click()
  await expect(firstDoc).toContainText('n: 2')
  // A broken stage says why, on that stage.
  await body(3, '{ n: "sideways" }')
  await expect(pipe.getByTestId('stage-3-output')).toContainText(/sort/i)
  await body(3, '{ n: -1, _id: 1 }')
  // $out isn't run as a preview.
  await pipe.getByRole('button', { name: 'Add a stage at the end' }).click()
  await pipe.getByLabel('Stage 4 operator').selectOption('$out')
  await expect(pipe.getByTestId('stage-4-output')).toContainText("writes documents, so it isn't previewed")
  await pipe.getByRole('button', { name: 'Remove stage 4' }).click()
  await shot(page, 'pipeline')
  // The whole pipeline, as a command.
  await pipe.getByRole('button', { name: 'Open in query' }).click()
  const cmd = page.getByTestId('sql-editor').last().locator('.cm-content')
  await expect(cmd).toContainText('db.people.aggregate([')
  await expect(cmd).toContainText('{ $group: { _id: "$city", n: { $sum: 1 } } }')
  await page.keyboard.press('Control+Shift+Enter')
  await expect(page.getByRole('gridcell', { name: 'Berlin', exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')

  // A time series collection, from the New collection dialog.
  await page.getByRole('button', { name: 'New collection' }).click()
  const nc = page.getByRole('dialog', { name: 'New collection' })
  await nc.getByRole('textbox', { name: 'Collection name' }).fill('readings')
  await nc.getByLabel('Collection kind').selectOption('timeseries')
  await nc.getByLabel('Meta field').fill('sensor')
  await nc.getByLabel('Granularity').selectOption('minutes')
  await expect(nc.getByTestId('create-preview')).toHaveText('db.createCollection("readings", {"timeseries":{"timeField":"timestamp","metaField":"sensor","granularity":"minutes"}})')
  // Settings that don't fit are explained before anything runs.
  await nc.getByLabel('Collection kind').selectOption('capped')
  await nc.getByLabel('Capped size').fill('')
  await expect(nc.getByTestId('create-preview')).toContainText('needs its size')
  await expect(nc.getByRole('button', { name: 'Create' })).toBeDisabled()
  await nc.getByLabel('Collection kind').selectOption('timeseries')
  await shot(page, 'new-timeseries')
  await nc.getByRole('button', { name: 'Create' }).click()
  const readings = page.getByTestId('data-readings')
  await readings.getByRole('tab', { name: 'Structure' }).click()
  await expect(readings.getByTestId('collection-options')).toHaveText('Time series on timestamp, by sensor, minutes.')

  await run('db.dropDatabase()')
  expect(errors).toEqual([])
})
