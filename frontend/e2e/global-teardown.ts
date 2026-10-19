import { readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'

export default async function globalTeardown() {
  const dir = process.env.GOTABLE_TMP
  if (!dir) return
  try { process.kill(Number(readFileSync(join(dir, 'pid'), 'utf8'))) } catch { /* already gone */ }
  rmSync(dir, { recursive: true, force: true })
}
