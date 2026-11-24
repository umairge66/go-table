import { expect, test, type Page } from '@playwright/test'

// Elasticsearch and OpenSearch through the UI: requests, SQL, an index's
// documents, an edit, a query. Set GOTABLE_E2E_ELASTIC to space-separated
// URLs, e.g. elasticsearch://127.0.0.1:9200 opensearch://127.0.0.1:9201
const urls = (process.env.GOTABLE_E2E_ELASTIC ?? '').split(/\s+/).filter(Boolean)

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

test('Elasticsearch: connect, requests, browse, edit (needs GOTABLE_E2E_ELASTIC)', async ({ browser }) => {
  test.skip(!urls.length, 'set GOTABLE_E2E_ELASTIC')
  test.setTimeout(urls.length * 120_000)
  for (const url of urls) {
    const label = url.startsWith('opensearch') ? 'OpenSearch' : 'Elasticsearch'
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(process.env.GOTABLE_URL!)
    const form = page.getByLabel('Connection form')
    await page.keyboard.press('Control+n')
    await form.getByRole('radio', { name: label, exact: true }).click()
    await expect(form.getByLabel('Port')).toHaveValue('9200')
    await expect(form.getByLabel('Database', { exact: true })).toHaveCount(0)
    await form.getByRole('button', { name: 'Paste URL' }).click()
    await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
    await page.getByRole('button', { name: 'Fill in' }).click()
    await form.getByRole('button', { name: 'Test' }).first().click()
    await expect(form.getByText(new RegExp(`${label} \\d`))).toBeVisible()
    await form.getByRole('button', { name: 'Save & connect' }).click()
    await expect(page.getByTestId('server-info').locator('visible=true')).toContainText(label)

    const run = async (text: string) => {
      await page.keyboard.press('Control+t')
      const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
      await editor.click()
      await editor.fill(text)
      await page.keyboard.press('Control+Shift+Enter')
    }
    await run(`DELETE /gt_e2e?ignore_unavailable=true
PUT /gt_e2e
{ "settings": { "number_of_replicas": 0 }, "mappings": { "properties": {
  "name": { "type": "text", "fields": { "keyword": { "type": "keyword" } } },
  "score": { "type": "integer" }, "seen": { "type": "date" }, "tags": { "type": "keyword" } } } }
POST /gt_e2e/_bulk?refresh=true
{"index":{"_id":"1"}}
{"name":"Alice","score":90,"seen":"2024-03-15T10:20:30Z","tags":["a","b"]}
{"index":{"_id":"2"}}
{"name":"Bob","score":70}

SELECT name, score FROM gt_e2e ORDER BY score DESC`)
    await expect(page.getByRole('gridcell', { name: 'Alice', exact: true }).first()).toBeVisible()
    await shot(page, `${label.toLowerCase()}-editor`)
    // Beautify indents a request's body.
    await page.getByTestId('sql-editor').last().locator('.cm-content').fill('GET /gt_e2e/_search {"size":1}')
    await page.keyboard.press('Control+Shift+f')
    await page.keyboard.press('Control+w')
    const discard = page.getByRole('button', { name: /Discard|Close/ })
    if (await discard.first().isVisible().catch(() => false)) await discard.first().click()

    await page.getByRole('button', { name: 'Refresh tables' }).click()
    await page.getByRole('navigation', { name: 'Tables' }).getByRole('button', { name: 'gt_e2e', exact: true }).click()
    const view = page.getByTestId('data-gt_e2e')
    const cell = (name: string) => view.getByRole('gridcell', { name, exact: true })
    await expect(cell('Bob')).toBeVisible()
    await expect(cell('["a","b"]')).toBeVisible()
    await shot(page, `${label.toLowerCase()}-table`)

    await cell('Bob').dblclick()
    await page.keyboard.press('Control+a')
    await page.keyboard.type('Bob"by')
    await page.keyboard.press('Enter')
    // A date is text here: a calendar would change its format.
    await cell('2024-03-15T10:20:30Z').dblclick()
    await expect(page.getByTestId('cell-picker')).toHaveCount(0)
    await page.keyboard.press('Escape')
    await page.keyboard.press('Control+s')
    const review = page.getByRole('dialog', { name: 'Review changes' })
    await expect(review.getByText(/POST \/gt_e2e\/_update\/2 — set \{"name":"Bob\\"by"\}/)).toBeVisible()
    await review.getByRole('button', { name: 'Commit' }).click()
    await expect(review).toBeHidden()
    await expect(cell('Bob"by')).toBeVisible()

    await view.getByRole('tab', { name: 'Structure' }).click()
    await expect(view.getByText('score', { exact: true })).toBeVisible()
    await expect(view.getByRole('button', { name: 'Add column' })).toHaveCount(0)
    await view.getByRole('tab', { name: 'Definition' }).click()
    await expect(view.getByText('PUT /gt_e2e').first()).toBeVisible()
    await shot(page, `${label.toLowerCase()}-definition`)

    await run('DELETE /gt_e2e')
    expect(errors).toEqual([])
    await page.close()
  }
})
