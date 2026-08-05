/**
 * ApiBridge abstracts the Wails runtime surface the adapter talks to.
 *
 * Wails injects two objects into the WebView window:
 *  - `window.go.main.App`: the generated Go binding methods (one property per
 *    exported App method), and
 *  - `window.runtime`: runtime helpers such as EventsOn/EventsOff.
 *
 * The generated `wailsjs/go/main/App.js` files are themselves just thin
 * re-exports of `window.go.main.App`, so the bridge calls that runtime object
 * directly. This keeps the adapter free of hand-maintained copies of the
 * generated binding interface (the artifacts are produced by `wails dev` /
 * `wails build`). When the generated bindings are absent (unit tests, plain
 * browser), every call fails explicitly with NOT_IMPLEMENTED.
 */

/** Error thrown for every binding the Go backend has not wired up yet. */
export interface NotImplementedError extends Error {
  code: 'NOT_IMPLEMENTED'
}

export function notImplementedError(method: string): NotImplementedError {
  const err = new Error(`NOT_IMPLEMENTED: ${method}`) as NotImplementedError
  err.name = 'NotImplementedError'
  err.code = 'NOT_IMPLEMENTED'
  return err
}

export interface ApiBridge {
  /** Invoke a Go binding method with positional args. */
  call<T = unknown>(method: string, ...args: unknown[]): Promise<T>
  /** Subscribe to a runtime event; returns an unsubscribe function. */
  on<T = unknown>(event: string, cb: (payload: T) => void): () => void
  /** Resolve the OS path for a File from a drag-drop (WebView2 `File.path`). */
  getPathForFile(file: File): string
}

type GoApp = Record<string, (...args: unknown[]) => Promise<unknown>>

interface WailsRuntime {
  /** Wails v2.13+ returns a per-listener unsubscribe; older runtimes return void. */
  EventsOn?: (name: string, cb: (...args: unknown[]) => void) => (() => void) | void
  EventsOff?: (name: string) => void
}

function wailsWindow(): {
  go?: { main?: { App?: GoApp } }
  runtime?: WailsRuntime
} {
  if (typeof window === 'undefined') return {}
  return window as { go?: { main?: { App?: GoApp } }; runtime?: WailsRuntime }
}

/**
 * Default bridge backed by the Wails-injected `window.go` / `window.runtime`.
 * Degrades safely when running outside a Wails WebView: bindings fail with
 * NOT_IMPLEMENTED, event subscription becomes a no-op unsubscribe.
 */
export class WailsBridge implements ApiBridge {
  async call<T = unknown>(method: string, ...args: unknown[]): Promise<T> {
    const app = wailsWindow().go?.main?.App
    const fn = app?.[method]
    if (typeof fn !== 'function') {
      throw notImplementedError(method)
    }
    return fn(...args) as Promise<T>
  }

  on<T = unknown>(event: string, cb: (payload: T) => void): () => void {
    const runtime = wailsWindow().runtime
    if (!runtime?.EventsOn) {
      console.warn(`[api] Wails runtime unavailable; event '${event}' will never fire`)
      return () => undefined
    }
    const unsubscribe = runtime.EventsOn(event, (payload) => cb(payload as T))
    let off = false
    return () => {
      if (off) return
      off = true
      if (typeof unsubscribe === 'function') {
        unsubscribe()
      } else {
        runtime.EventsOff?.(event)
      }
    }
  }

  getPathForFile(file: File): string {
    const path = (file as File & { path?: string }).path
    if (typeof path !== 'string' || path.length === 0) {
      throw notImplementedError('getPathForFile')
    }
    return path
  }
}
