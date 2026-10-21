import { expect, test, type Page } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// The desktop window offers the system file dialogs through the Go side.
// This server stands them in with scripted ones (GOTABLE_TEST_DIALOGS), so
// the UI's native save/open path runs for real and files land on disk.
test.describe.configure({ mode: 'serial' })

let page: Page
let proc: ChildProcess
let dir: string
const errors: string[] = []

test.beforeAll(async ({ browser }) => {
  dir = mkdtempSync(join(tmpdir(), 'gotable-native-'))
  const bin = resolve(dirname(fileURLToPath(import.meta.url)), '../../bin/gotable')
  proc = spawn(bin, ['-no-window', '-no-keyring', '-config', join(dir, 'connections.json'), '-addr', '127.0.0.1:0'],
    { stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, GOTABLE_TEST_DIALOGS: dir } })
  const url = await new Promise<string>((ok, fail) => {
    const t = setTimeout(() => fail(new Error('gotable did not start')), 10_000)
    proc.stdout!.on('data', (b: Buffer) => { const m = /(http:\/\/\S+)/.exec(b.toString()); if (m) { clearTimeout(t); ok(m[1]) } })
  })
  // A desktop: the window is smaller than the screen (not maximized).
  page = await browser.newPage({ screen: { width: 1920, height: 1080 } })
  page.on('pageerror', (e) => errors.push(e.message))
  // Chrome's own picker must not be used when native dialogs exist.
  await page.addInitScript(() => { (window as unknown as Record<string, unknown>).showSaveFilePicker = () => { throw new Error('browser picker used') } })
  await page.goto(url)
})

test.afterAll(async () => {
  proc?.kill()
  rmSync(dir, { recursive: true, force: true })
  expect(errors).toEqual([])
})

const saved = (name: string) => join(dir, 'saved', name)

test('native dialogs: export, save a result, import a picked file', async () => {
  // A SQLite connection with some data.
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder('/home/you/data/app.sqlite').fill(join(dir, 'native.db'))
  await form.getByRole('button', { name: 'Save & connect' }).click()
  const sidebar = page.getByRole('navigation', { name: 'Tables' })
  await expect(sidebar).toBeVisible()
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill("CREATE TABLE fruit (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO fruit (name) VALUES ('apple'), ('pear');")
  await page.keyboard.press('Control+Shift+Enter')
  await sidebar.getByTestId('sidebar-fruit').click()

  // Table export: cancelling the dialog starts nothing; then it's written to disk by Go.
  const view = page.getByTestId('data-fruit')
  await view.getByRole('button', { name: 'Export' }).click()
  writeFileSync(join(dir, 'cancel-next'), '')
  await page.getByRole('button', { name: 'Export CSV…' }).click()
  await expect(page.getByRole('dialog', { name: 'Export fruit' })).toBeVisible()
  expect(existsSync(saved('fruit.csv'))).toBe(false)
  await page.getByRole('button', { name: 'Export CSV…' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Export saved' })).toBeVisible()
  expect(readFileSync(saved('fruit.csv'), 'utf8').trim().split('\n')).toEqual(['id,name', '1,apple', '2,pear'])

  // A query result saved from the editor.
  await page.keyboard.press('Control+t')
  await editor.click()
  await editor.fill('SELECT name FROM fruit ORDER BY id')
  await page.keyboard.press('Control+Shift+Enter')
  await page.getByRole('button', { name: 'Save as .sql file…' }).click()
  await expect.poll(() => existsSync(saved('query.sql'))).toBe(true)
  expect(readFileSync(saved('query.sql'), 'utf8')).toBe('SELECT name FROM fruit ORDER BY id')

  // Import: the Open dialog picks a file on disk; it's read in place (no upload).
  const script = join(dir, 'more.sql')
  writeFileSync(script, "INSERT INTO fruit (name) VALUES ('plum');\nINSERT INTO fruit (name) VALUES ('fig');\n")
  writeFileSync(join(dir, 'open-next'), script)
  await page.getByRole('button', { name: 'Import / Export database' }).click()
  await page.getByRole('dialog', { name: 'Export database' }).getByRole('tab', { name: 'Import' }).click()
  const importDialog = page.getByRole('dialog', { name: 'Import into database' })
  await expect(importDialog.getByText('Very large file?')).toHaveCount(0) // no path field needed
  await importDialog.getByRole('button', { name: 'Choose a file' }).click()
  await expect(importDialog.getByTestId('picked-file')).toContainText('more.sql')
  await expect(importDialog.getByTestId('picked-file')).toContainText(script)
  await importDialog.getByRole('button', { name: 'Start import' }).click()
  await expect(importDialog.getByTestId('job-progress')).toHaveAttribute('data-status', 'done')
  await page.keyboard.press('Escape')
  await page.keyboard.press('Control+t')
  await editor.click()
  await editor.fill('SELECT COUNT(*) FROM fruit')
  await page.keyboard.press('Control+Shift+Enter')
  await expect(page.getByTestId('grid').last().getByRole('gridcell').first()).toHaveText('4')
})

test('own title bar: window buttons, dragging and resizing', async () => {
  const log = join(dir, 'window.log')
  const actions = () => (existsSync(log) ? readFileSync(log, 'utf8').trim().split('\n') : [])
  const header = page.locator('header').first()
  await expect(header.getByRole('button', { name: 'Minimize' })).toBeVisible()
  await expect(header.getByRole('button', { name: 'Maximize' })).toBeVisible()
  await expect(header.getByRole('button', { name: 'Close' })).toBeVisible()

  // Empty header space moves the window; a double-click maximizes it.
  const box = (await header.boundingBox())!
  const y = box.y + box.height / 2
  // An empty spot of the bar: not on a button, field or label.
  const x = await page.evaluate(({ left, right, y }) => {
    for (let x = right - 40; x > left; x -= 6) {
      const el = document.elementFromPoint(x, y)
      if (el && el.closest('header') && !el.closest('button, input, a, [data-no-drag]')) return x
    }
    return -1
  }, { left: box.x, right: box.x + box.width, y })
  expect(x).toBeGreaterThan(0)
  const empty = { x, y }
  await page.mouse.click(empty.x, empty.y)
  await expect.poll(actions).toEqual(['drag'])
  await page.mouse.dblclick(empty.x, empty.y)
  await expect.poll(actions).toContain('toggle-maximize')
  // Controls in the bar still work and don't move the window.
  const before = actions().length
  await header.getByRole('button', { name: 'Minimize' }).click()
  await expect.poll(actions).toHaveLength(before + 1)
  expect(actions().at(-1)).toBe('minimize')

  // The edges resize the window.
  const vp = page.viewportSize()!
  await page.mouse.move(vp.width - 1, vp.height - 1)
  await page.mouse.down()
  await page.mouse.up()
  await expect.poll(() => actions().at(-1)).toBe('resize-se')
  await page.mouse.move(vp.width / 2, 1)
  await page.mouse.down()
  await page.mouse.up()
  await expect.poll(() => actions().at(-1)).toBe('resize-n')

  await header.getByRole('button', { name: 'Close' }).click()
  await expect.poll(() => actions().at(-1)).toBe('close')
})

test('native dialogs: browse for an SSH private key, checked as it is picked', async () => {
  await page.getByRole('button', { name: 'Connections', exact: true }).click()
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'PostgreSQL' }).click()
  await form.getByRole('switch').click()
  const keyField = form.getByPlaceholder('~/.ssh/id_ed25519')

  // A file that isn't a key is flagged right away.
  const notKey = join(dir, 'notes.txt')
  writeFileSync(notKey, 'hello')
  writeFileSync(join(dir, 'open-next'), notKey)
  await form.getByRole('button', { name: 'Choose the key file' }).click()
  await expect(keyField).toHaveValue(notKey)
  await expect(form.getByRole('alert')).toHaveText('this file is not an SSH private key')

  // An encrypted key asks for its passphrase until one is typed.
  const key = join(dir, 'id_ed25519')
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', 'pp', '-f', key])
  writeFileSync(join(dir, 'open-next'), key)
  await form.getByRole('button', { name: 'Choose the key file' }).click()
  await expect(keyField).toHaveValue(key)
  await expect(form.getByRole('alert')).toHaveCount(0)
  await expect(form.getByText('This key is encrypted: enter its passphrase.')).toBeVisible()
  await form.getByPlaceholder('if the key is encrypted').fill('pp')
  await expect(form.getByText('This key is encrypted: enter its passphrase.')).toHaveCount(0)

  // A cancelled dialog leaves the path as it was.
  rmSync(join(dir, 'open-next'))
  await form.getByRole('button', { name: 'Choose the key file' }).click()
  await expect(keyField).toHaveValue(key)
})
