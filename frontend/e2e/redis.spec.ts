import { expect, test, type Page } from '@playwright/test'

// Redis through the UI: key patterns, a key's contents, edits and the
// command editor. Set GOTABLE_E2E_REDIS to a URL, e.g.
//   redis://:pw@127.0.0.1:6379/3
const url = process.env.GOTABLE_E2E_REDIS ?? ''

const shots = process.env.GOTABLE_E2E_SHOTS ?? ''
const shot = async (page: Page, name: string) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }) }

async function connect(page: Page) {
  await page.goto(process.env.GOTABLE_URL!)
  const form = page.getByLabel('Connection form')
  await page.keyboard.press('Control+n')
  // Both tiles can be picked.
  for (const name of ['Redis', 'Valkey']) {
    await form.getByRole('radio', { name, exact: true }).click()
    await expect(form.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true')
  }
  await expect(form.getByLabel('Port')).toHaveValue('6379')
  await form.getByRole('button', { name: 'Paste URL' }).click()
  await page.getByPlaceholder('postgres://user:password@localhost:5432/shop').fill(url)
  await page.getByRole('button', { name: 'Fill in' }).click()
  await expect(form.getByRole('radio', { name: /^(Redis|Valkey)$/ }).and(page.locator('[aria-checked="true"]'))).toBeVisible()
  await form.getByRole('button', { name: 'Test' }).first().click()
  await expect(form.getByText(/(Redis|Valkey) \d/)).toBeVisible()
  await form.getByRole('button', { name: 'Save & connect' }).click()
  await expect(page.getByTestId('server-info').locator('visible=true')).toContainText(/Redis|Valkey/)
}

const runner = (page: Page) => async (text: string) => {
  await page.keyboard.press('Control+t')
  const editor = page.getByTestId('sql-editor').last().locator('.cm-content')
  await editor.click()
  await editor.fill(text)
  await page.keyboard.press('Control+Shift+Enter')
}

test('Redis: connect, commands, key lists, a key\'s contents, edits (needs GOTABLE_E2E_REDIS)', async ({ page }) => {
  test.skip(!url, 'set GOTABLE_E2E_REDIS')
  test.setTimeout(120_000)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await connect(page)
  const run = runner(page)

  await run(`FLUSHDB
SET user:1 Alice
HSET user:1:profile name Alice city Berlin
RPUSH queue a b c
ZADD scores 10 alice 20 bob
SET counter 5
EXPIRE counter 600
HGETALL user:1:profile`)
  await expect(page.getByRole('gridcell', { name: 'Berlin', exact: true })).toBeVisible()
  await shot(page, 'redis-query')
  await page.keyboard.press('Control+w')

  // The sidebar: every key, and the namespaces in them.
  await page.getByRole('button', { name: 'Refresh tables' }).click()
  const nav = page.getByRole('navigation', { name: 'Tables' })
  await expect(nav.getByRole('button', { name: 'user:*', exact: true })).toBeVisible()
  await nav.getByRole('button', { name: 'All keys', exact: true }).click()
  const keys = page.getByTestId('data-*')
  const cell = (v: ReturnType<Page['getByTestId']>, name: string) => v.getByRole('gridcell', { name, exact: true })
  await expect(cell(keys, 'queue')).toBeVisible()
  await expect(cell(keys, '‹hash: 2 fields›')).toBeVisible()
  await expect(cell(keys, 'sorted set')).toBeVisible()
  await shot(page, 'redis-keys')

  // A string is edited where it is listed; the TTL is kept.
  await cell(keys, '5').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('6')
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  await expect(review.getByText('SET counter 6 KEEPTTL')).toBeVisible()
  await expect(review.getByText(/MULTI\/EXEC/)).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(review).toBeHidden()
  await expect(cell(keys, '6')).toBeVisible()

  // Following a key opens its contents.
  await cell(keys, 'user:1:profile').hover()
  await keys.getByRole('button', { name: 'Open key = user:1:profile' }).click()
  const hash = page.getByTestId('data-user:1:profile')
  await expect(cell(hash, 'Berlin')).toBeVisible()
  await cell(hash, 'Berlin').dblclick()
  await page.keyboard.press('Control+a')
  await page.keyboard.type('Paris')
  await page.keyboard.press('Enter')
  await page.keyboard.press('Control+s')
  await expect(review.getByText('HSET user:1:profile city Paris')).toBeVisible()
  await review.getByRole('button', { name: 'Commit' }).click()
  await expect(review).toBeHidden()
  await shot(page, 'redis-hash')

  // The Commands tab writes the key again.
  await hash.getByRole('tab', { name: 'Commands' }).click()
  await expect(hash.getByText(/HSET user:1:profile/)).toBeVisible()
  await expect(hash.getByRole('tab', { name: 'Structure' })).toHaveCount(0)

  await run('HGET user:1:profile city')
  await expect(page.getByRole('gridcell', { name: 'Paris', exact: true })).toBeVisible()
  await page.keyboard.press('Control+w')
  await run('FLUSHDB')
  expect(errors).toEqual([])
})
