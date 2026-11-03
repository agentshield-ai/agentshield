import { describe, expect, it, vi } from 'vitest'
import { UndoQueue, type Clock } from './undo'

function fakeClock() {
  let now = 0
  let seq = 0
  const timers = new Map<number, { at: number; fn: () => void }>()
  const clock: Clock = {
    now: () => now,
    setTimeout: (fn, ms) => {
      const id = ++seq
      timers.set(id, { at: now + ms, fn })
      return id as unknown as ReturnType<typeof setTimeout>
    },
    clearTimeout: (t) => void timers.delete(t as unknown as number),
  }
  const advance = async (ms: number) => {
    now += ms
    for (const [id, t] of [...timers])
      if (t.at <= now) {
        timers.delete(id)
        t.fn()
      }
    await Promise.resolve()
    await Promise.resolve()
  }
  return { clock, advance }
}

describe('UndoQueue', () => {
  it('hides at once and deletes after the undo window', async () => {
    const { clock, advance } = fakeClock()
    const q = new UndoQueue(clock, 6000)
    const apply = vi.fn()
    const commit = vi.fn()
    q.schedule({ message: 'Deleted view', apply, commit })
    expect(apply).toHaveBeenCalledOnce()
    expect(q.pending()).toEqual([{ id: 1, message: 'Deleted view', expiresAt: 6000 }])
    await advance(5999)
    expect(commit).not.toHaveBeenCalled()
    await advance(1)
    expect(commit).toHaveBeenCalledOnce()
    expect(q.pending()).toEqual([])
  })

  it('Undo restores the item and never deletes it', async () => {
    const { clock, advance } = fakeClock()
    const q = new UndoQueue(clock, 6000)
    const revert = vi.fn()
    const commit = vi.fn()
    const id = q.schedule({ message: 'Deleted score', revert, commit })
    expect(q.undo(id)).toBe(true)
    expect(revert).toHaveBeenCalledOnce()
    await advance(10_000)
    expect(commit).not.toHaveBeenCalled()
    expect(q.undo(id)).toBe(false)
  })

  it('reverts and reports when the deletion fails', async () => {
    const { clock, advance } = fakeClock()
    const q = new UndoQueue(clock, 100)
    const revert = vi.fn()
    const onError = vi.fn()
    q.schedule({ message: 'x', revert, onError, commit: () => Promise.reject(new Error('403')) })
    await advance(100)
    expect(revert).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0][0]).toBeInstanceOf(Error)
  })

  it('flushes pending deletions when the page is hidden, and notifies listeners', async () => {
    const { clock } = fakeClock()
    const q = new UndoQueue(clock, 6000)
    const seen: number[] = []
    q.subscribe((p) => seen.push(p.length))
    const a = vi.fn()
    const b = vi.fn()
    q.schedule({ message: 'a', commit: a })
    q.schedule({ message: 'b', commit: b })
    await q.flush()
    expect(a).toHaveBeenCalledOnce()
    expect(b).toHaveBeenCalledOnce()
    expect(seen).toEqual([1, 2, 1, 0])
  })
})
