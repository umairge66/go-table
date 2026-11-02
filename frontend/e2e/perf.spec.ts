import { expect, test } from '@playwright/test'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// Measures the data grid on a wide table (31 columns × 2,000 rows): arrow-key
// moves and scrolling like dragging the scroll thumb. Opt-in, as timings
// depend on the machine: GOTABLE_PERF=1 npx playwright test e2e/perf.spec.ts
test('grid performance (needs GOTABLE_PERF)', async ({ browser }) => {
  test.skip(!process.env.GOTABLE_PERF, 'set GOTABLE_PERF=1 to measure the grid')
  test.setTimeout(180_000)
  const file = join(process.env.GOTABLE_TMP!, 'perf.sqlite')
  const db = new DatabaseSync(file)
  const cols = Array.from({ length: 30 }, (_, i) => `c${i} TEXT`).join(', ')
  db.exec(`DROP TABLE IF EXISTS wide; CREATE TABLE wide (id INTEGER PRIMARY KEY, ${cols})`)
  const ins = db.prepare(`INSERT INTO wide VALUES (?, ${Array(30).fill('?').join(', ')})`)
  db.exec('BEGIN')
  for (let r = 1; r <= 2000; r++) ins.run(r, ...Array.from({ length: 30 }, (_, i) => `value ${r}-${i} lorem ipsum`))
  db.exec('COMMIT')
  db.close()

  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await page.goto(process.env.GOTABLE_URL!)
  await page.keyboard.press('Control+n')
  const form = page.getByLabel('Connection form')
  await form.getByRole('radio', { name: 'SQLite' }).click()
  await form.getByPlaceholder(/app\.sqlite/).fill(file)
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'wide', exact: true }).click()
  const view = page.getByTestId('data-wide')
  // Show all 2,000 rows on one page.
  await view.getByLabel('Rows per page').selectOption('5000')
  await expect(view.getByText(/1–2,000 of 2,000/)).toBeVisible()
  await view.getByRole('gridcell').nth(1).click()

  // Arrow keys: time 60 presses, each until the next frame is painted.
  const keys = await page.evaluate(async () => {
    const grid = document.querySelector('[data-testid="grid"]') as HTMLElement
    const frame = () => new Promise((r) => requestAnimationFrame(() => r(null)))
    const times: number[] = []
    for (let i = 0; i < 60; i++) {
      const t = performance.now()
      grid.dispatchEvent(new KeyboardEvent('keydown', { key: i < 40 ? 'ArrowDown' : 'ArrowRight', bubbles: true }))
      await frame()
      times.push(performance.now() - t)
    }
    times.sort((a, b) => a - b)
    return { median: times[30], p90: times[54], max: times[59] }
  })

  // Scrolling: move the scroll position like dragging the thumb, one step
  // per frame, and time the frames.
  const scroll = await page.evaluate(async () => {
    const grid = document.querySelector('[data-testid="grid"]') as HTMLElement
    const frame = () => new Promise((r) => requestAnimationFrame(() => r(null)))
    const times: number[] = []
    let last = performance.now()
    for (let i = 1; i <= 80; i++) {
      grid.scrollTop = (grid.scrollHeight - grid.clientHeight) * (i / 80)
      await frame()
      const now = performance.now()
      times.push(now - last)
      last = now
    }
    times.sort((a, b) => a - b)
    return { median: times[40], p90: times[72], max: times[79], slow: times.filter((t) => t > 33).length }
  })
  const dom = await page.evaluate(() => document.querySelectorAll('[role="gridcell"]').length)
  console.log('PERF', JSON.stringify({ keys, scroll, cellsInDom: dom }))
  // Generous limits (a frame is ~16 ms); before the grid drew only visible
  // columns and memoised rows, scrolling took ~49 ms a frame.
  expect(scroll.median, 'median scroll frame (ms)').toBeLessThan(30)
  expect(keys.median, 'median arrow-key move (ms)').toBeLessThan(30)
  expect(dom, 'cells drawn').toBeLessThan(600)
})
