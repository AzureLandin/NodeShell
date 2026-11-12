// @vitest-environment jsdom
import { StrictMode, type ReactNode } from 'react'
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useConfirmationQueue } from '../../src/renderer/src/app/use-confirmation-queue'

describe('application confirmation ownership', () => {
  it('queues concurrent callers and ignores duplicate or stale completions', async () => {
    const { result } = renderHook(() => useConfirmationQueue())
    let first!: Promise<boolean>
    let second!: Promise<boolean>
    act(() => {
      first = result.current.ask({ title: 'First', message: 'One' })
      second = result.current.ask({ title: 'Second', message: 'Two' })
    })
    const firstId = result.current.current!.id
    expect(result.current.current?.title).toBe('First')
    act(() => {
      result.current.settle(firstId, true)
      result.current.settle(firstId, false)
    })
    await expect(first).resolves.toBe(true)
    expect(result.current.current?.title).toBe('Second')
    act(() => result.current.settle(firstId, true))
    expect(result.current.current?.title).toBe('Second')
    act(() => result.current.settle(result.current.current!.id, false))
    await expect(second).resolves.toBe(false)
    expect(result.current.current).toBeNull()
  })

  it('settles every waiting caller on unmount and rejects retained requests', async () => {
    const view = renderHook(() => useConfirmationQueue())
    let first!: Promise<boolean>
    let second!: Promise<boolean>
    act(() => {
      first = view.result.current.ask({ title: 'First', message: 'One' })
      second = view.result.current.ask({ title: 'Second', message: 'Two' })
    })
    const ask = view.result.current.ask
    view.unmount()
    await expect(first).resolves.toBe(false)
    await expect(second).resolves.toBe(false)
    await expect(ask({ title: 'Disposed', message: 'No owner' })).resolves.toBe(false)
  })

  it('accepts requests after StrictMode effect replay', async () => {
    const wrapper = ({ children }: { children: ReactNode }): React.JSX.Element => (
      <StrictMode>{children}</StrictMode>
    )
    const { result } = renderHook(() => useConfirmationQueue(), { wrapper })
    let answer!: Promise<boolean>
    act(() => {
      answer = result.current.ask({ title: 'Active', message: 'Ready' })
    })
    act(() => result.current.settle(result.current.current!.id, true))
    await expect(answer).resolves.toBe(true)
  })
})
