import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

/**
 * jsdom missing-browser-primitive stubs (T1.8.3). This setup runs for every
 * test file in its own environment: node-env tests get only the jest-dom
 * matchers, jsdom-env tests additionally get the stubs below. Nothing here
 * mocks component behavior or window.api — tests supply their own fake api.
 */

// Vitest globals are disabled in this project, so @testing-library/react's
// auto-cleanup (which hooks the global afterEach) never fires; without this
// the DOM accumulates renders across tests within a file.
afterEach(() => cleanup())

if (typeof window !== 'undefined') {
  // ModalShell's prefersReducedMotion() reads this on mount. Returning
  // `matches: true` makes modals enter/leave their open phase synchronously
  // (no rAF/setTimeout dance), which is a legitimate reduced-motion config.
  window.matchMedia = ((query: string) => ({
    matches: true,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false
  })) as typeof window.matchMedia

  // @radix-ui/react-select (via @floating-ui) observes the viewport for
  // scroll-buffer logic.
  class ResizeObserverStub {
    observe = (): void => {}
    unobserve = (): void => {}
    disconnect = (): void => {}
  }
  window.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver

  // Radix scrolls the focused option into view; jsdom does not implement it.
  if (typeof Element !== 'undefined') {
    Element.prototype.scrollIntoView = () => undefined
    // @radix-ui/react-select calls these on pointerdown targets.
    Element.prototype.hasPointerCapture = () => false
    Element.prototype.setPointerCapture = () => undefined
    Element.prototype.releasePointerCapture = () => undefined
  }
}
