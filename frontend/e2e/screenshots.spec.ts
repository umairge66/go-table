import { expect, test, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Takes the README screenshots (docs/screenshots/{light,dark}/NN-*.png), in
// both themes, from a fresh GoTable each. Needs a PostgreSQL database with
// scripts/screenshot-demo.sql loaded:
//
//   GOTABLE_SCREENSHOTS=../docs/screenshots \
//   GOTABLE_SCREENSHOT_PG=postgres://postgres:pw@127.0.0.1:5432/shop \
//   npx playwright test e2e/screenshots.spec.ts
const out = process.env.GOTABLE_SCREENSHOTS
const pg = process.env.GOTABLE_SCREENSHOT_PG

async function start(): Promise<{ proc: ChildProcess; url: string }> {
  const dir = mkdtempSync(join(tmpdir(), 'gotable-shots-'))
  const bin = resolve(dirname(fileURLToPath(import.meta.url)), '../../bin/gotable')
  const proc = spawn(bin, ['-no-window', '-no-keyring', '-config', join(dir, 'connections.json'), '-addr', '127.0.0.1:0'], { stdio: ['ignore', 'pipe', 'inherit'] })
  const url = await new Promise<string>((ok, fail) => {
    const t = setTimeout(() => fail(new Error('gotable did not start')), 10_000)
    proc.stdout!.on('data', (b: Buffer) => { const m = /(http:\/\/\S+)/.exec(b.toString()); if (m) { clearTimeout(t); ok(m[1]) } })
  })
  return { proc, url }
}

for (const theme of ['light', 'dark'] as const) {
  test(`screenshots (${theme}) (needs GOTABLE_SCREENSHOTS)`, async ({ browser }) => {
    test.skip(!out || !pg, 'set GOTABLE_SCREENSHOTS to the output folder and GOTABLE_SCREENSHOT_PG to the demo database')
    test.setTimeout(120_000)
    const dir = resolve(out!, theme)
    mkdirSync(dir, { recursive: true })
    const { proc, url } = await start()
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: theme })
    // Draw the window's own title bar and buttons, as the desktop app does.
    await page.route('**/api/native', (r) => r.fulfill({ json: { dialogs: true, window: true } }))
    const shot = async (name: string) => {
      await page.mouse.move(1439, 899) // no hover effects
      await page.waitForTimeout(350) // animations settle
      await page.screenshot({ path: join(dir, `${name}.png`) })
    }
    try {
      await scenes(page, url, shot)
    } finally {
      await page.close()
      proc.kill()
    }
  })
}

async function scenes(page: Page, url: string, shot: (name: string) => Promise<void>) {
  const u = new URL(pg!)
  await page.goto(url)
  await expect(page.getByText('Welcome to GoTable')).toBeVisible()
  await shot('01-welcome')

  // The connection form: every database, then a tested connection.
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await shot('02-form')
  await form.getByPlaceholder('e.g. Shop production').fill('Shop demo')
  await form.getByRole('radio', { name: 'Development' }).click()
  await form.getByRole('textbox', { name: 'Host' }).fill(u.hostname)
  await form.getByRole('textbox', { name: 'Port' }).fill(u.port || '5432')
  await form.getByRole('textbox', { name: 'User' }).fill(decodeURIComponent(u.username))
  await form.getByLabel('Password').fill(decodeURIComponent(u.password))
  await form.getByRole('textbox', { name: 'Database' }).fill(u.pathname.slice(1))
  await form.getByRole('button', { name: 'Test' }).click()
  await expect(form.getByRole('status')).toContainText('Connected successfully')
  await form.getByRole('status').scrollIntoViewIfNeeded()
  await shot('03-form-tested')

  // The workspace, then a table.
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByText('Jump anywhere')).toBeVisible()
  await shot('04-workspace')
  const sidebar = page.getByRole('navigation', { name: 'Tables' })
  await sidebar.getByRole('button', { name: 'customers', exact: true }).click()
  const view = page.getByTestId('data-customers')
  await expect(view.getByRole('gridcell').first()).toBeVisible()
  await shot('05-table')

  // Filters, an edit waiting to be committed, and the row inspector.
  await view.getByRole('button', { name: /^Filter/ }).click()
  await view.getByLabel('Raw WHERE').fill("created_at > now() - interval '7 days'").catch(() => {})
  const city = view.locator('[data-row="0"] [data-col="3"]')
  await city.dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('Paris (VIP)')
  await page.keyboard.press('Enter')
  await view.locator('[data-row="0"] [data-col="1"]').click()
  await expect(view.getByText('1 unsaved change')).toBeVisible()
  await shot('06-edit-filter')

  // The command palette.
  await page.keyboard.press('Control+k')
  await page.getByLabel('Search commands').fill('ord')
  await shot('07-palette')
  await page.keyboard.press('Escape')

  // A query and its result.
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill('SELECT c.city, count(*) AS orders, sum(o.total) AS revenue\nFROM orders o\nJOIN customers c ON c.id = o.customer_id\nWHERE c.city IS NOT NULL\nGROUP BY c.city\nORDER BY revenue DESC;')
  await page.keyboard.press('Control+Enter')
  await expect(page.getByText(/5 row\(s\) returned|Result 1/).first()).toBeVisible()
  await shot('08-query')

  // The table designer.
  await page.getByRole('button', { name: 'New table' }).click()
  const designer = page.getByLabel('New table form')
  await designer.getByLabel('Table name').fill('invoices')
  await designer.getByLabel('Table comment').fill('Invoices sent to customers')
  const add = async (name: string, type: string) => {
    await designer.getByRole('button', { name: /Add a column/ }).click()
    const names = designer.getByLabel('Column name')
    await names.last().fill(name)
    await designer.getByLabel('Column type').last().selectOption({ label: type }).catch(async () => {
      await designer.getByLabel('Column type').last().fill(type)
    })
  }
  const names = designer.getByLabel('Column name')
  await names.nth(1).fill('customer_id')
  await designer.getByLabel('Column type').nth(1).selectOption({ label: 'integer' }).catch(() => {})
  await designer.getByLabel('Index').nth(1).selectOption('index')
  await add('amount', 'numeric')
  await add('status', 'text')
  await designer.getByLabel('Default kind').last().selectOption('value')
  await designer.getByLabel('Default value').last().fill('open')
  await add('issued_at', 'timestamptz')
  await designer.getByLabel('Default kind').last().selectOption('expression')
  await designer.getByLabel('Default value').last().fill('now()')
  await designer.getByLabel('Table name').click()
  await shot('09-new-table')
}
