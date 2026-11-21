import { expect, test } from '@playwright/test'
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { createHash, generateKeyPairSync, sign } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Updating GoTable from GitHub: a stand-in for GitHub serves a signed
// release, and a GoTable of its own (a .tar.gz install at version 0.8.0)
// finds, installs and reports it.

let gh: Server
let app: ChildProcess
let url = ''
let target = ''

test.beforeAll(async () => {
  const dir = mkdtempSync(join(tmpdir(), 'gotable-update-'))
  // The release: the Linux archive with a new binary, its checksum, signed.
  const stage = join(dir, 'stage', 'gotable-0.9.0')
  mkdirSync(stage, { recursive: true })
  writeFileSync(join(stage, 'gotable'), '#!new gotable 0.9.0\n')
  const name = 'gotable-0.9.0-linux-amd64.tar.gz'
  execFileSync('tar', ['-C', join(dir, 'stage'), '-czf', join(dir, name), 'gotable-0.9.0'])
  const archive = readFileSync(join(dir, name))
  const sums = `${createHash('sha256').update(archive).digest('hex')}  ${name}\n`
  const { publicKey, privateKey } = generateKeyPairSync('ed25519')
  const sig = sign(null, Buffer.from(sums), privateKey).toString('base64')
  const pub = publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64')
  const files: Record<string, Buffer | string> = { [name]: archive, SHA256SUMS: sums, 'SHA256SUMS.sig': sig }
  gh = createServer((req, res) => {
    const base = `http://127.0.0.1:${(gh.address() as { port: number }).port}`
    if (req.url === '/latest') {
      res.setHeader('Content-Type', 'application/json')
      res.end(JSON.stringify({ tag_name: 'v0.9.0', html_url: 'https://github.com/umairge66/go-table/releases/tag/v0.9.0', published_at: '2026-11-21T10:00:00Z',
        body: '### Faster grids\n\nTables scroll faster.\n\n- One\n- Two `code`',
        assets: Object.entries(files).map(([n, d]) => ({ name: n, browser_download_url: `${base}/dl/${n}`, size: Buffer.byteLength(d) })) }))
      return
    }
    const f = files[decodeURIComponent((req.url ?? '').replace('/dl/', ''))]
    if (!f) { res.statusCode = 404; res.end(); return }
    res.end(f)
  })
  await new Promise<void>((ok) => gh.listen(0, '127.0.0.1', ok))
  const port = (gh.address() as { port: number }).port

  // The GoTable being updated: its "binary" is a file in a folder of its own.
  target = join(dir, 'install', 'gotable')
  mkdirSync(dirname(target))
  writeFileSync(target, '#!old gotable 0.8.0\n', { mode: 0o755 })
  const bin = resolve(dirname(fileURLToPath(import.meta.url)), '../../bin/gotable')
  app = spawn(bin, ['-no-window', '-no-keyring', '-config', join(dir, 'config', 'connections.json'), '-addr', '127.0.0.1:0'], {
    stdio: ['ignore', 'pipe', 'inherit'],
    env: { ...process.env, GOTABLE_UPDATE_API: `http://127.0.0.1:${port}/latest`, GOTABLE_UPDATE_KEY: pub, GOTABLE_UPDATE_TARGET: target, GOTABLE_VERSION: 'v0.8.0' },
  })
  url = await new Promise<string>((ok, fail) => {
    const t = setTimeout(() => fail(new Error('gotable did not start')), 10_000)
    app.stdout!.on('data', (b: Buffer) => { const m = /(http:\/\/\S+)/.exec(b.toString()); if (m) { clearTimeout(t); ok(m[1]) } })
  })
})

test.afterAll(() => { app?.kill(); gh?.close() })

test('a newer release is found, skipped, brought back and installed', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(url)
  // Checked by itself on the first start (never checked before).
  const notice = page.getByTestId('update-notice')
  await expect(notice).toContainText('GoTable 0.9.0 is available')
  await expect(notice).toContainText('You have 0.8.0')
  await notice.getByRole('button', { name: 'What’s new' }).click()
  await expect(notice.getByTestId('update-notes')).toContainText('Faster grids')
  await expect(notice.getByTestId('update-notes').locator('li')).toHaveCount(2)
  await page.screenshot({ path: `${process.env.GOTABLE_E2E_SHOTS ?? tmpdir()}/update-notice.png` })

  // Skip this version: gone, and Settings can bring it back.
  await notice.getByRole('button', { name: 'Skip this version' }).click()
  await expect(notice).toBeHidden()
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  const dlg = page.getByRole('dialog', { name: 'Settings' })
  await dlg.getByRole('button', { name: 'Updates' }).click()
  await expect(dlg.getByTestId('update-version')).toContainText('GoTable 0.8.0')
  await expect(dlg.getByTestId('update-version')).toContainText('Linux archive')
  await expect(dlg.getByText('You skipped GoTable 0.9.0.')).toBeVisible()
  await dlg.getByRole('button', { name: 'Show it again' }).click()
  await dlg.getByRole('button', { name: 'Check now' }).click()
  await expect(dlg.getByTestId('update-status')).toContainText('GoTable 0.9.0 is available')
  await expect(dlg.getByTestId('update-status')).toContainText('last checked just now')
  await dlg.getByRole('button', { name: 'Done' }).click()

  // Update: downloaded, checked against its signature, put in place.
  await expect(notice).toBeVisible()
  await notice.getByRole('button', { name: 'Update' }).click()
  await expect(notice).toContainText('GoTable 0.9.0 is installed', { timeout: 15_000 })
  await expect(notice.getByRole('button', { name: 'Restart now' })).toBeVisible()
  expect(readFileSync(target, 'utf8')).toBe('#!new gotable 0.9.0\n')
  expect(errors).toEqual([])
})
