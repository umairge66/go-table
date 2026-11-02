import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// The grid's layout: a narrow table fills the view; the row inspector opens
// and closes without leaving the grid mis-sized or losing its place.

async function open(page: Page, file: string, table: string) {
  await page.goto(process.env.GOTABLE_URL!)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(file)
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: table, exact: true }).click()
  return page.getByTestId(`data-${table}`)
}

const layout = (page: Page) => page.evaluate(() => {
  const grid = document.querySelector('[data-testid="grid"]') as HTMLElement
  const heads = [...grid.querySelectorAll('[role="columnheader"]')] as HTMLElement[]
  const row = grid.querySelector('[data-row]') as HTMLElement
  return {
    view: grid.clientWidth,
    columnsEnd: Math.round(heads[heads.length - 1].getBoundingClientRect().right - grid.getBoundingClientRect().left),
    rowWidth: row.offsetWidth,
    widths: heads.map((h) => Math.round(h.offsetWidth)),
    overflow: grid.style.overflow,
    scrollTop: grid.scrollTop,
    hScroll: grid.scrollWidth > grid.clientWidth,
  }
})

test('a narrow table fills the view; the inspector toggles cleanly', async ({ browser }) => {
  const file = join(process.env.GOTABLE_TMP!, 'layout.sqlite')
  const db = new DatabaseSync(file)
  db.exec('DROP TABLE IF EXISTS kv; CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT, expiration INTEGER)')
  const ins = db.prepare('INSERT INTO kv VALUES (?, ?, ?)')
  for (let i = 0; i < 300; i++) ins.run(`key-${i}`, `value ${i}`, 1772564343 + i)
  db.close()

  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } })
  const view = await open(page, file, 'kv')
  await expect(view.getByRole('gridcell').first()).toBeVisible()

  // Three short columns stretch to the edge, without a horizontal scrollbar
  // (once the grid has measured itself).
  await expect.poll(async () => { const x = await layout(page); return x.columnsEnd >= x.view - 1 && !x.hScroll }).toBe(true)
  let l = await layout(page)
  const wide = l.widths

  // Scroll down, then hide and show the inspector (Ctrl+Shift+I): the
  // columns re-fit the new width and the grid keeps its place.
  await view.getByRole('gridcell').first().click()
  await page.evaluate(() => { (document.querySelector('[data-testid="grid"]') as HTMLElement).scrollTop = 2000 })
  for (const _ of [0, 1]) {
    await page.keyboard.press('Control+Shift+I')
    await expect.poll(async () => (await layout(page)).view).not.toBe(l.view)
    await expect.poll(async () => { const x = await layout(page); return x.columnsEnd >= x.view - 1 && x.overflow === '' }).toBe(true)
    l = await layout(page)
    expect(l.scrollTop).toBe(2000)
    expect(l.hScroll).toBe(false)
  }
  expect(l.widths).toEqual(wide)

  // Resizing a column by hand keeps exact widths; rows still span the view.
  const handle = view.getByRole('columnheader', { name: /^value/ }).locator('div').last()
  const box = (await handle.boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2 - 150, box.y + box.height / 2, { steps: 5 })
  await page.mouse.up()
  l = await layout(page)
  expect(Math.abs(l.widths[1] - (wide[1] - 150))).toBeLessThanOrEqual(2)
  expect(l.widths[0]).toBe(wide[0])
  expect(l.columnsEnd).toBeLessThan(l.view - 140)
  expect(l.rowWidth).toBeGreaterThanOrEqual(l.view)
  await page.close()
})
