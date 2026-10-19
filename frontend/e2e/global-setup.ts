import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Starts the real binary with an empty config and in-memory secrets, and
// shares its start URL (with the one-time token) with the tests.
export default async function globalSetup() {
  const dir = mkdtempSync(join(tmpdir(), 'gotable-e2e-'))
  const bin = resolve(dirname(fileURLToPath(import.meta.url)), '../../bin/gotable')
  const proc = spawn(bin, ['-no-window', '-no-keyring', '-config', join(dir, 'connections.json'), '-addr', '127.0.0.1:0'], { stdio: ['ignore', 'pipe', 'inherit'] })
  const url = await new Promise<string>((ok, fail) => {
    const t = setTimeout(() => fail(new Error('gotable did not start')), 10_000)
    proc.stdout!.on('data', (b: Buffer) => {
      const m = /(http:\/\/\S+)/.exec(b.toString())
      if (m) { clearTimeout(t); ok(m[1]) }
    })
    proc.on('exit', (code) => fail(new Error(`gotable exited with ${code}`)))
  })
  process.env.GOTABLE_URL = url
  process.env.GOTABLE_TMP = dir
  writeFileSync(join(dir, 'pid'), String(proc.pid))
}
