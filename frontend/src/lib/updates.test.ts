import { expect, test } from 'vitest'
import type { UpdateProgress } from '../api'
import { showNotice, type UpdateState } from './updates'

const state = (phase: UpdateProgress['phase'], more: Partial<UpdateState> = {}): UpdateState =>
  ({ checking: false, progress: { phase, done: 0, total: 0 }, dismissed: false, restarting: false, ...more })
const check = { available: true, latest: 'v0.9.0' } as UpdateState['check']

test('the notice closes when dismissed, except while an update is under way', () => {
  for (const phase of ['ready', 'opened', 'failed'] as const) {
    expect(showNotice(state(phase), '')).toBe(true)
    expect(showNotice(state(phase, { dismissed: true, check }), '')).toBe(false)
  }
  for (const phase of ['downloading', 'installing'] as const) expect(showNotice(state(phase, { dismissed: true }), '')).toBe(true)
  expect(showNotice(state('ready', { dismissed: true, restarting: true }), '')).toBe(true)
})

test('an update found shows unless dismissed or skipped', () => {
  expect(showNotice(state('idle'), '')).toBe(false)
  expect(showNotice(state('idle', { check }), '')).toBe(true)
  expect(showNotice(state('idle', { check }), 'v0.9.0')).toBe(false)
  expect(showNotice(state('idle', { check }), 'v0.8.0')).toBe(true)
  expect(showNotice(state('idle', { check, dismissed: true }), '')).toBe(false)
})
