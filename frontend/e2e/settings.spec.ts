import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'

// Settings: theme, accent colour, the editor's text size and keyboard
// shortcuts; saved by the app so the next launch (a new address, so empty
// browser storage) has them.

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

const putSettings = (page: Page, body: string) => page.evaluate((b) =>
  fetch('/api/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: b }), body)
const cssVar = (page: Page, name: string) => page.evaluate((n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim(), name)
const theme = (page: Page) => page.evaluate(() => document.documentElement.dataset.theme)

test('settings: theme, accent, editor text size and shortcuts, kept for the next launch', async ({ browser }) => {
  test.setTimeout(90_000)
  const page = await browser.newPage()
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(process.env.GOTABLE_URL!)
  await putSettings(page, '{}')
  await page.reload()
  try {
    // ---- Appearance
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    const dlg = page.getByRole('dialog', { name: 'Settings' })
    await dlg.getByRole('radio', { name: 'Dark' }).click()
    expect(await theme(page)).toBe('dark')
    await dlg.getByRole('radio', { name: 'Light' }).click()
    expect(await theme(page)).toBe('light')
    // Purple (indigo) stays the default, with the stylesheet's own colours.
    expect(await cssVar(page, '--accent')).toBe('#5b5bd6')

    await dlg.getByRole('radio', { name: 'Teal' }).click()
    expect(await cssVar(page, '--accent')).toBe('#0f8a7e')
    await expect(dlg.getByTestId('accent-preview').getByRole('button', { name: 'Commit' })).toHaveCSS('background-color', 'rgb(15, 138, 126)')
    await shot(page, 'settings-appearance')
    // Each preset has its own shade for dark mode.
    await dlg.getByRole('radio', { name: 'Dark' }).click()
    expect(await cssVar(page, '--accent')).toBe('#3fd0c0')
    await shot(page, 'settings-appearance-dark')
    await dlg.getByRole('radio', { name: 'Light' }).click()
    // A custom colour; text on a light one turns dark to stay readable.
    await dlg.getByLabel('Custom accent colour').fill('#ffcc00')
    expect(await cssVar(page, '--accent')).toBe('#ffcc00')
    expect(await cssVar(page, '--accent-fg')).toBe('#111116')
    await dlg.getByRole('radio', { name: 'Rose' }).click()

    // ---- Editor text size
    await dlg.getByRole('button', { name: 'Editor' }).click()
    for (let i = 0; i < 3; i++) await dlg.getByRole('button', { name: 'Larger default text' }).click()
    await expect(dlg.getByTestId('default-font-size')).toHaveText('16 px')
    await expect(dlg.getByTestId('font-preview').locator('.cm-editor')).toHaveCSS('font-size', '16px')
    await shot(page, 'settings-editor')

    // ---- Shortcuts
    await dlg.getByRole('button', { name: 'Keyboard shortcuts' }).click()
    const row = (id: string) => dlg.getByTestId(`shortcut-${id}`)
    await row('home.new').getByRole('button', { name: 'Change the keys for New connection' }).click()
    await page.keyboard.press('Control+Shift+Y')
    await expect(row('home.new')).toContainText('Ctrl Shift Y')
    await expect(row('home.new')).toContainText('changed')
    // Keys every text field needs are refused.
    await row('ws.tables').getByRole('button', { name: /Change the keys/ }).click()
    await page.keyboard.press('Control+c')
    await expect(row('ws.tables').getByRole('alert')).toContainText('kept for its usual job')
    await expect(row('ws.tables')).toContainText('Ctrl P')
    // A clash shows on both shortcuts until one moves.
    await row('ws.newQuery').getByRole('button', { name: /Change the keys/ }).click()
    await page.keyboard.press('Control+k')
    await expect(row('ws.newQuery').getByRole('alert')).toContainText('Jump to table or command')
    await expect(row('ws.palette').getByRole('alert')).toContainText('New query tab')
    await shot(page, 'settings-shortcuts')
    await row('ws.newQuery').getByRole('button', { name: 'Reset New query tab' }).click()
    await expect(row('ws.palette').getByRole('alert')).toHaveCount(0)
    // Esc while recording cancels it and leaves the dialog open.
    await row('editor.format').getByRole('button', { name: /Change the keys/ }).click()
    await page.keyboard.press('Escape')
    await expect(dlg).toBeVisible()
    await expect(row('editor.format')).toContainText('Ctrl I')
    // The editor runs with other keys.
    await row('editor.runCurrent').getByRole('button', { name: /Change the keys/ }).click()
    await page.keyboard.press('Control+e')
    await expect(row('editor.runCurrent')).toContainText('Ctrl E')
    // Searching finds by name or keys.
    await dlg.getByRole('textbox', { name: 'Search shortcuts' }).fill('ctrl shift y')
    await expect(dlg.getByTestId(/^shortcut-/)).toHaveCount(1)
    await dlg.getByRole('button', { name: 'Done' }).click()

    // ---- Kept by the app, not the browser: the next launch has a new
    // address and empty storage.
    await page.evaluate(() => localStorage.clear())
    await page.reload()
    await expect.poll(() => cssVar(page, '--accent')).toBe('#d1294f')
    expect(await theme(page)).toBe('light')

    // The new keys work, the old ones no longer do. (A saved connection's
    // form may already show: a new one says so.)
    const starting = page.getByText('Choose a database, then tell GoTable where to find it.')
    await page.keyboard.press('Control+n')
    await expect(starting).toBeHidden()
    await page.keyboard.press('Control+Shift+Y')
    await expect(starting).toBeVisible()
    const form = page.getByLabel('Connection form')
    await form.getByRole('radio', { name: 'SQLite' }).click()
    await form.getByPlaceholder(/app\.sqlite/).fill(join(process.env.GOTABLE_TMP!, 'settings.sqlite'))
    await form.getByRole('button', { name: 'Save & connect' }).click()
    await expect(page.getByText('Jump anywhere')).toBeVisible()

    // ---- The query editor: the default size, its own zoom, the new run keys.
    await page.keyboard.press('Control+t')
    const editor = page.getByTestId('sql-editor').last()
    const size = page.getByTestId('editor-font-size')
    await expect(size).toHaveText('16')
    await expect(editor.locator('.cm-editor')).toHaveCSS('font-size', '16px')
    await expect(page.getByRole('button', { name: /^Run/ }).first()).toContainText('Ctrl E')
    await editor.locator('.cm-content').click()
    await editor.locator('.cm-content').fill('SELECT 41 + 1 AS answer')
    await page.keyboard.press('Control+=')
    await page.keyboard.press('Control+=')
    await expect(size).toHaveText('18')
    await expect(editor.locator('.cm-editor')).toHaveCSS('font-size', '18px')
    await page.getByRole('button', { name: /^Smaller text/ }).click()
    await expect(size).toHaveText('17')
    await page.keyboard.press('Control+0')
    await expect(size).toHaveText('16')
    await editor.locator('.cm-content').click()
    await page.keyboard.press('Control+Enter') // no longer runs (the editor's own new line instead)
    await expect(page.getByRole('gridcell', { name: '42', exact: true })).toBeHidden()
    await editor.locator('.cm-line', { hasText: 'SELECT 41' }).click() // back on the statement
    await page.keyboard.press('Control+e')
    await expect(page.getByRole('gridcell', { name: '42', exact: true })).toBeVisible()
    await shot(page, 'settings-query')

    // A new default reaches open editors that weren't zoomed; Ctrl+, opens Settings.
    await page.keyboard.press('Control+,')
    await dlg.getByRole('button', { name: 'Editor' }).click()
    await dlg.getByRole('button', { name: 'Smaller default text' }).click()
    await dlg.getByRole('button', { name: 'Done' }).click()
    await expect(size).toHaveText('15')
    await expect(editor.locator('.cm-editor')).toHaveCSS('font-size', '15px')

    // ---- Panels: how the rail, the sidebar and the inspector start.
    const openSettingsConnection = async () => {
      await page.getByPlaceholder('Search connections').fill('settings')
      await page.getByPlaceholder('Search connections').press('Enter')
      await expect(page.getByTestId('server-info').locator('visible=true')).toContainText('SQLite')
    }
    const restart = async () => { await page.evaluate(() => localStorage.clear()); await page.reload() }
    const button = (name: RegExp) => page.getByRole('button', { name })
    const isOn = async (name: RegExp) => /\bbg-active\b/.test(await button(name).first().getAttribute('class') ?? '')
    await page.keyboard.press('Control+,')
    await dlg.getByRole('button', { name: 'Panels' }).click()
    await dlg.getByTestId('panel-rail').getByRole('radio', { name: 'Closed' }).click()
    await dlg.getByTestId('panel-sidebar').getByRole('radio', { name: 'Closed' }).click()
    await dlg.getByTestId('panel-inspector').getByRole('radio', { name: 'Open' }).click()
    await shot(page, 'settings-panels')
    await dlg.getByRole('button', { name: 'Done' }).click()
    await restart()
    await openSettingsConnection()
    await expect(button(/^Show databases/)).toBeVisible()
    await expect(button(/^Show sidebar/)).toBeVisible()
    await expect(page.getByRole('navigation', { name: 'Tables' })).toBeHidden()
    expect(await isOn(/^Row inspector/)).toBe(true)
    // "As I left it": shown again after a restart, then hidden again.
    await page.keyboard.press('Control+,')
    await dlg.getByRole('button', { name: 'Panels' }).click()
    await dlg.getByTestId('panel-sidebar').getByRole('radio', { name: 'As I left it' }).click()
    await dlg.getByRole('button', { name: 'Done' }).click()
    await page.keyboard.press('Control+b')
    await expect(page.getByRole('navigation', { name: 'Tables' })).toBeVisible()
    await page.evaluate(() => new Promise((r) => setTimeout(r, 400))) // the save is debounced
    await restart()
    await openSettingsConnection()
    await expect(page.getByRole('navigation', { name: 'Tables' })).toBeVisible()
    await expect(button(/^Show databases/)).toBeVisible() // still "Closed"
    await page.keyboard.press('Control+b')
    await expect(page.getByRole('navigation', { name: 'Tables' })).toBeHidden()
    await page.evaluate(() => new Promise((r) => setTimeout(r, 400)))
    await restart()
    await openSettingsConnection()
    await expect(page.getByRole('navigation', { name: 'Tables' })).toBeHidden()
  } finally {
    await putSettings(page, '{}') // the other tests expect the defaults
    await page.close()
  }
  expect(errors).toEqual([])
})
