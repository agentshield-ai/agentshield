// Undo over confirm (DESIGN.md §7.1 rule 11). A deletion hides the item at
// once and commits after a few seconds unless the user presses Undo. Pending
// commits are flushed when the page is hidden, so nothing is silently lost.

export interface UndoAction {
  /** Shown in the toast, e.g. "Deleted view “Slow errors”". */
  message: string
  /** Runs now: hide the item (optimistic update). */
  apply?: () => void
  /** Runs when Undo is pressed: show the item again. */
  revert?: () => void
  /** Runs when the undo window closes: the real deletion. */
  commit: () => Promise<unknown> | void
  /** Runs if the commit fails (after revert). */
  onError?: (e: unknown) => void
}

export interface PendingUndo {
  id: number
  message: string
  expiresAt: number
}

type Timer = ReturnType<typeof setTimeout>

export interface Clock {
  now: () => number
  setTimeout: (fn: () => void, ms: number) => Timer
  clearTimeout: (t: Timer) => void
}

const realClock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
}

export const UNDO_MS = 6000

export class UndoQueue {
  private seq = 0
  private items = new Map<number, { action: UndoAction; timer: Timer; expiresAt: number }>()
  private listeners = new Set<(p: PendingUndo[]) => void>()

  constructor(
    private clock: Clock = realClock,
    private delay = UNDO_MS,
  ) {}

  schedule(action: UndoAction): number {
    const id = ++this.seq
    action.apply?.()
    const expiresAt = this.clock.now() + this.delay
    const timer = this.clock.setTimeout(() => void this.commit(id), this.delay)
    this.items.set(id, { action, timer, expiresAt })
    this.emit()
    return id
  }

  /** Undo a pending action; false when it already committed. */
  undo(id: number): boolean {
    const it = this.items.get(id)
    if (!it) return false
    this.clock.clearTimeout(it.timer)
    this.items.delete(id)
    it.action.revert?.()
    this.emit()
    return true
  }

  async commit(id: number): Promise<void> {
    const it = this.items.get(id)
    if (!it) return
    this.clock.clearTimeout(it.timer)
    this.items.delete(id)
    this.emit()
    try {
      await it.action.commit()
    } catch (e) {
      it.action.revert?.()
      it.action.onError?.(e)
    }
  }

  /** Commit everything now (page hide / unload). */
  flush(): Promise<void[]> {
    return Promise.all([...this.items.keys()].map((id) => this.commit(id)))
  }

  pending(): PendingUndo[] {
    return [...this.items.entries()].map(([id, it]) => ({ id, message: it.action.message, expiresAt: it.expiresAt }))
  }

  subscribe(fn: (p: PendingUndo[]) => void): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  private emit() {
    const p = this.pending()
    for (const l of this.listeners) l(p)
  }
}

/** The app-wide queue the toast host shows. */
export const undoQueue = new UndoQueue()
