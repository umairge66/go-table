import { describe, expect, it } from 'vitest'
import { layout, type Box, type Link } from './diagramLayout'

const box = (id: string, h = 100): Box => ({ id, w: 200, h })
const overlaps = (boxes: Box[], at: ReturnType<typeof layout>) => {
  const bad: string[] = []
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
    const a = boxes[i], b = boxes[j], p = at[a.id], q = at[b.id]
    if (p.x < q.x + b.w && q.x < p.x + a.w && p.y < q.y + b.h && q.y < p.y + a.h) bad.push(`${a.id}/${b.id}`)
  }
  return bad
}

describe('ER layout', () => {
  it('puts referenced tables left of those that refer to them', () => {
    const boxes = ['orders', 'customers', 'items', 'products', 'alone'].map((x) => box(x))
    const links: Link[] = [{ from: 'orders', to: 'customers' }, { from: 'items', to: 'orders' }, { from: 'items', to: 'products' }]
    const at = layout(boxes, links)
    expect(at.customers.x).toBeLessThan(at.orders.x)
    expect(at.orders.x).toBeLessThan(at.items.x)
    expect(at.products.x).toBeLessThan(at.items.x)
    expect(Object.keys(at)).toHaveLength(5)
    expect(overlaps(boxes, at)).toEqual([])
  })
  it('copes with circles, self references and unknown tables', () => {
    const boxes = ['a', 'b', 'c'].map((x) => box(x))
    const at = layout(boxes, [{ from: 'a', to: 'b' }, { from: 'b', to: 'c' }, { from: 'c', to: 'a' }, { from: 'a', to: 'a' }, { from: 'a', to: 'gone' }])
    expect(overlaps(boxes, at)).toEqual([])
  })
  it('never overlaps, however many tables', () => {
    const boxes = Array.from({ length: 300 }, (_, i) => box(`t${i}`, 60 + (i % 13) * 30))
    const links = Array.from({ length: 260 }, (_, i) => ({ from: `t${i}`, to: `t${(i * 7) % 90}` }))
    const at = layout(boxes, links)
    expect(Object.keys(at)).toHaveLength(300)
    expect(overlaps(boxes, at)).toEqual([])
    expect(layout(boxes, links)).toEqual(at) // the same every time
  })
})
