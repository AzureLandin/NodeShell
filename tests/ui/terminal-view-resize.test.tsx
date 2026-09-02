// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import { TerminalView } from '../../src/renderer/src/components/TerminalView'
import { MAX_WRITE_CHARS_PER_FRAME } from '../../src/renderer/src/terminal-output'
import { burstPayloads } from './terminal-stress'
import { installFakeApi, type FakeApi } from './helpers'
import type { ResolvedTheme } from '../../src/shared/types'

interface MockTermInstance {
  cols: number
  rows: number
  options: Record<string, unknown>
  loadAddon: ReturnType<typeof vi.fn>
  open: ReturnType<typeof vi.fn>
  write: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  onData: ReturnType<typeof vi.fn>
  clear: ReturnType<typeof vi.fn>
  clearSelection: ReturnType<typeof vi.fn>
  getSelection: ReturnType<typeof vi.fn>
  paste: ReturnType<typeof vi.fn>
}

interface MockWebglAddonInstance {
  onContextLoss: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  contextLossCb: (() => void) | null
}

const mockInstances: MockTermInstance[] = []
const mockWebglInstances: MockWebglAddonInstance[] = []
let fitMockFn: () => void

vi.mock('@xterm/xterm', () => {
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown> = {}
    loadAddon = vi.fn()
    open = vi.fn()
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn(() => ({ dispose: vi.fn() }))
    clear = vi.fn()
    clearSelection = vi.fn()
    getSelection = vi.fn(() => '')
    paste = vi.fn()

    constructor() {
      mockInstances.push(this as unknown as MockTermInstance)
    }
  }
  return { Terminal: MockTerminal }
})

vi.mock('@xterm/addon-fit', () => {
  class MockFitAddon {
    fit = vi.fn(() => {
      fitMockFn()
    })
  }
  return { FitAddon: MockFitAddon }
})

vi.mock('@xterm/addon-webgl', () => {
  class MockWebglAddon {
    onContextLoss = vi.fn((cb: () => void) => {
      this.contextLossCb = cb
      return { dispose: vi.fn() }
    })
    dispose = vi.fn()
    contextLossCb: (() => void) | null = null

    constructor() {
      mockWebglInstances.push(this)
    }
  }
  return { WebglAddon: MockWebglAddon }
})

vi.mock('@xterm/addon-unicode-graphemes', () => {
  class MockUnicodeGraphemesAddon {}
  return { UnicodeGraphemesAddon: MockUnicodeGraphemesAddon }
})

const defaultTheme: ResolvedTheme = 'dark'

describe('TerminalView Resize State Machine and Output Governance', () => {
  let fake: FakeApi
  let resizeObserverCallback: ResizeObserverCallback | null = null

  beforeEach(() => {
    mockInstances.length = 0
    mockWebglInstances.length = 0
    fake = installFakeApi()
    fitMockFn = vi.fn()

    // Mock ResizeObserver
    globalThis.ResizeObserver = class {
      constructor(cb: ResizeObserverCallback) {
        resizeObserverCallback = cb
      }
      observe = vi.fn()
      unobserve = vi.fn()
      disconnect = vi.fn()
    } as unknown as typeof ResizeObserver
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('dispatches valid initial size to sessions.resize on first visible render', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = vi.fn((sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    })

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledWith('s1', 80, 24)
    })
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
  })

  it('deduplicates identical ResizeObserver sizes and sends only 1 resize', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledWith('s1', 80, 24)
    })
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)

    // Trigger ResizeObserver with the exact same dimensions multiple times
    act(() => {
      resizeObserverCallback?.([], {} as ResizeObserver)
      resizeObserverCallback?.([], {} as ResizeObserver)
      resizeObserverCallback?.([], {} as ResizeObserver)
    })

    // Advance timers for debounce
    await act(async () => {
      await new Promise((r) => setTimeout(r, 200))
    })

    // Should NOT send duplicate resize IPC
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
  })

  it('debounces rapid size changes and dispatches the final stable size', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledWith('s1', 80, 24)
    })
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)

    const term = mockInstances[0]!

    // Rapid intermediate changes
    act(() => {
      term.cols = 90
      term.rows = 28
      resizeObserverCallback?.([], {} as ResizeObserver)
    })

    act(() => {
      term.cols = 105
      term.rows = 32
      resizeObserverCallback?.([], {} as ResizeObserver)
    })

    act(() => {
      term.cols = 120
      term.rows = 40
      resizeObserverCallback?.([], {} as ResizeObserver)
    })

    // Wait for debounce window (120ms) + RAF
    await act(async () => {
      await new Promise((r) => setTimeout(r, 200))
    })

    // Only the final size should be dispatched
    expect(fake.mocks.sessions.resize).toHaveBeenCalledWith('s1', 120, 40)
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(2) // 1 initial + 1 final
  })

  it('does NOT send resize when terminal is invisible (visible=false)', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={false}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    act(() => {
      resizeObserverCallback?.([], {} as ResizeObserver)
    })

    await act(async () => {
      await new Promise((r) => setTimeout(r, 200))
    })

    expect(fake.mocks.sessions.resize).not.toHaveBeenCalled()
  })

  it('dispatches resize when transitioning from visible=false to visible=true', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    const { rerender } = render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={false}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    expect(fake.mocks.sessions.resize).not.toHaveBeenCalled()

    // Become visible
    rerender(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledWith('s1', 80, 24)
    })
  })

  it('handles synchronous throw in sessions.resize without freezing and retries automatically', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    // Synchronously throws
    fake.mocks.sessions.resize.mockImplementationOnce(() => {
      throw new Error('Bridge crashed synchronously')
    })

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
    })

    // Automatic retry timer (500ms) will retry and succeed without user interaction
    await act(async () => {
      await new Promise((r) => setTimeout(r, 600))
    })

    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(2)
    expect(fake.mocks.sessions.resize).toHaveBeenLastCalledWith('s1', 80, 24)
  })

  it('automatically retries resize on failure after delay without manual user resize', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    // Rejected promise
    fake.mocks.sessions.resize.mockRejectedValueOnce(new Error('IPC network glitch'))

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
    })

    const term = mockInstances[0]!
    expect(term.dispose).not.toHaveBeenCalled()

    // Wait for auto-retry delay
    await act(async () => {
      await new Promise((r) => setTimeout(r, 600))
    })

    // Second call should have automatically retried and succeeded
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(2)
    expect(fake.mocks.sessions.resize).toHaveBeenLastCalledWith('s1', 80, 24)
  })

  it('handles WebGL context loss cleanly and continues working with DOM renderer', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = (_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    expect(mockWebglInstances.length).toBe(1)
    const webgl = mockWebglInstances[0]!
    expect(webgl.onContextLoss).toHaveBeenCalled()

    // Trigger WebGL context loss callback
    act(() => {
      webgl.contextLossCb?.()
    })

    // WebGL addon should have been safely disposed
    expect(webgl.dispose).toHaveBeenCalledTimes(1)

    // Terminal must still accept writes, input, and resizes
    const term = mockInstances[0]!
    act(() => {
      capturedListener?.('post-context-loss-data')
    })

    await waitFor(() => {
      expect(term.write).toHaveBeenCalledWith('post-context-loss-data')
    })
    expect(term.dispose).not.toHaveBeenCalled()
  })

  it('coalesces multiple data chunks arriving in one animation frame into a single write and tracks metrics', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = vi.fn((_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    })

    window.__TERMINAL_METRICS__ = {}

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    const term = mockInstances[0]!
    expect(capturedListener).toBeTruthy()

    // Emit multiple chunks in the same tick
    act(() => {
      capturedListener!('chunk-1\n')
      capturedListener!('chunk-2\n')
      capturedListener!('chunk-3\n')
    })

    await waitFor(() => {
      expect(term.write).toHaveBeenCalledWith('chunk-1\nchunk-2\nchunk-3\n')
    })
    expect(term.write).toHaveBeenCalledTimes(1)

    // Verify metrics telemetry
    expect(window.__TERMINAL_METRICS__['s1']).toEqual(
      expect.objectContaining({
        flushCount: 1,
        totalBytesWritten: 'chunk-1\nchunk-2\nchunk-3\n'.length
      })
    )
  })

  it('flushes pending output before dispose on unmount and prevents late callbacks from writing', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = vi.fn((_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    })

    const { unmount } = render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    const term = mockInstances[0]!

    // Add pending data right before unmount
    act(() => {
      capturedListener!('tail-output-data')
    })

    // Unmount
    unmount()

    // Must have flushed pending before term.dispose
    expect(term.write).toHaveBeenCalledWith('tail-output-data')
    expect(term.dispose).toHaveBeenCalledTimes(1)

    // Any late listener invocation must be ignored
    if (capturedListener) {
      act(() => {
        capturedListener!('late-data-ignored')
      })
    }
    expect(term.write).not.toHaveBeenCalledWith('late-data-ignored')
  })

  it('updates theme without triggering sessions.resize', async () => {
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    const { rerender } = render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    await waitFor(() => {
      expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
    })

    rerender(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme="light"
      />
    )

    const term = mockInstances[0]!
    expect(term.options.theme).toEqual(expect.objectContaining({ background: '#fafafa' }))
    // Should NOT have triggered a second resize
    expect(fake.mocks.sessions.resize).toHaveBeenCalledTimes(1)
  })

  it('does not invoke sessions.resize when unmounting immediately upon mount or queued resize', async () => {
    fake.mocks.sessions.resize.mockClear()
    const dataListeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      dataListeners.set(sessionId, cb)
      return () => dataListeners.delete(sessionId)
    }

    // Mount and unmount immediately in the same tick
    const { unmount } = render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )
    unmount()

    // Drain all microtasks, timers and animation frames
    await act(async () => {
      await new Promise((r) => setTimeout(r, 200))
    })

    // sessions.resize must NEVER have been called on unmounted component
    expect(fake.mocks.sessions.resize).not.toHaveBeenCalled()
  })

  it('caps a huge pending write across animation frames and concatenates in order', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = (_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    const term = mockInstances[0]!
    const big = `🚀${'x'.repeat(MAX_WRITE_CHARS_PER_FRAME)}尾`
    act(() => {
      capturedListener!(big)
    })

    await waitFor(() => {
      expect(term.write.mock.calls.length).toBeGreaterThan(1)
    })
    expect(term.write.mock.calls.map((call) => call[0] as string).join('')).toBe(big)
  })

  it('does not reload WebGL after context loss', async () => {
    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={() => () => undefined}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    expect(mockWebglInstances.length).toBe(1)
    const webgl = mockWebglInstances[0]!
    act(() => {
      webgl.contextLossCb?.()
      webgl.contextLossCb?.()
    })
    expect(webgl.dispose).toHaveBeenCalledTimes(1)
    expect(mockWebglInstances.length).toBe(1)
  })

  it('keeps writing background session output without sending resize', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = (_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    }

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={false}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    const term = mockInstances[0]!
    act(() => {
      capturedListener!('background-chunk')
    })
    await waitFor(() => {
      expect(term.write).toHaveBeenCalledWith('background-chunk')
    })
    expect(fake.mocks.sessions.resize).not.toHaveBeenCalled()
  })

  it('routes mixed output across eight sessions without crosstalk or extra instances', async () => {
    const ids = Array.from({ length: 8 }, (_, i) => `s${i + 1}`)
    const listeners = new Map<string, (data: string) => void>()
    const registerDataListener = (sessionId: string, cb: (data: string) => void) => {
      listeners.set(sessionId, cb)
      return () => listeners.delete(sessionId)
    }

    const { rerender, unmount } = render(
      <>
        {ids.map((id, index) => (
          <TerminalView
            key={id}
            sessionId={id}
            registerDataListener={registerDataListener}
            visible={index === 0}
            fontFamily="Consolas"
            fontSize={14}
            resolvedTheme={defaultTheme}
          />
        ))}
      </>
    )

    expect(mockInstances.length).toBe(8)

    for (const id of ids) {
      const payloads = burstPayloads(id, 6)
      act(() => {
        for (const payload of payloads) {
          listeners.get(id)?.(payload)
        }
      })
    }

    for (let i = 0; i < ids.length; i++) {
      rerender(
        <>
          {ids.map((id, index) => (
            <TerminalView
              key={id}
              sessionId={id}
              registerDataListener={registerDataListener}
              visible={index === i}
              fontFamily="Consolas"
              fontSize={14}
              resolvedTheme={defaultTheme}
            />
          ))}
        </>
      )
    }

    await waitFor(() => {
      for (let i = 0; i < ids.length; i++) {
        const written = mockInstances[i]!.write.mock.calls.map((call) => call[0] as string).join('')
        expect(written).toContain(`${ids[i]}-5:`)
        for (let j = 0; j < ids.length; j++) {
          if (i === j) continue
          expect(written).not.toContain(`${ids[j]}-`)
        }
      }
    })

    expect(mockInstances.length).toBe(8)
    for (const term of mockInstances) {
      expect(term.dispose).not.toHaveBeenCalled()
    }

    unmount()
    for (const term of mockInstances) {
      expect(term.dispose).toHaveBeenCalledTimes(1)
    }
    expect(listeners.size).toBe(0)
  })

  it('records diagnostic counters when window.__TERMINAL_METRICS__ is present', async () => {
    let capturedListener: ((data: string) => void) | null = null
    const registerDataListener = (_sessionId: string, cb: (data: string) => void) => {
      capturedListener = cb
      return () => {
        capturedListener = null
      }
    }
    window.__TERMINAL_METRICS__ = {}

    render(
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={true}
        fontFamily="Consolas"
        fontSize={14}
        resolvedTheme={defaultTheme}
      />
    )

    act(() => {
      capturedListener!('a')
      capturedListener!('b')
    })
    await waitFor(() => {
      expect(window.__TERMINAL_METRICS__?.['s1']?.writeCount).toBe(1)
    })
    expect(window.__TERMINAL_METRICS__?.['s1']).toEqual(
      expect.objectContaining({
        dataReceiveCount: 2,
        flushCount: 1,
        writeCount: 1,
        resizeIpcCount: 1
      })
    )
  })
})
