import clsx from 'clsx'
import { List } from 'lucide-react'
import { findType, typeGroups, type TypeSpec } from '../lib/sqlTypes'
import type { Driver } from '../types'
import { Select } from './ui'

const OTHER = '\u0000other'

/**
 * TypeSelect picks a column type from the database's grouped type list, or
 * takes a typed one ("Other…"). The bracket value (length, precision, values)
 * is edited separately; picking a type fills in its usual value.
 */
export function TypeSelect({ driver, value, custom = [], onChange, className }: {
  driver: Driver
  value: TypeSpec
  custom?: string[]
  onChange: (v: TypeSpec) => void
  className?: string
}) {
  const field = 'h-8 w-full min-w-0 rounded-md border border-line bg-panel px-2 font-mono text-[12.5px] focus:border-accent focus:outline-none'
  if (value.custom) {
    return (
      <div className={clsx('flex min-w-0 gap-1', className)}>
        <input className={field} value={value.base} placeholder="type, e.g. geography(Point,4326)" aria-label="Column type"
          autoFocus={!value.base} onChange={(e) => onChange({ ...value, base: e.target.value })} />
        <button type="button" title="Choose from the list" aria-label="Choose type from the list"
          className="flex size-8 shrink-0 items-center justify-center rounded-md border border-line text-muted hover:bg-hover hover:text-fg"
          onClick={() => onChange({ base: typeGroups(driver, custom)[0].types[0].name, param: '', unsigned: false, array: false, custom: false })}>
          <List className="size-3.5" />
        </button>
      </div>
    )
  }
  return (
    <Select className={clsx('min-w-0', className)} selectClassName="rounded-md font-mono text-[12.5px]" aria-label="Column type" value={value.base}
      onChange={(e) => {
        if (e.target.value === OTHER) { onChange({ base: '', param: '', unsigned: false, array: false, custom: true }); return }
        const t = findType(driver, e.target.value, custom)
        const initial = t?.param && 'initial' in t.param ? t.param.initial ?? '' : ''
        onChange({ base: e.target.value, param: initial, unsigned: value.unsigned && !!t?.traits?.includes('unsigned'), array: value.array, custom: false })
      }}>
      {typeGroups(driver, custom).map((g) => (
        <optgroup key={g.label} label={g.label}>
          {g.types.map((t) => <option key={t.name} value={t.name} title={t.hint}>{t.name}</option>)}
        </optgroup>
      ))}
      <optgroup label="Something else">
        <option value={OTHER}>Other… (type it)</option>
      </optgroup>
    </Select>
  )
}
