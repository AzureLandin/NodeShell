# Terminal Right-Click Context Menu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use compose:subagent to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a right-click context menu to the xterm terminal with Copy, Paste, and Clear Screen, implemented purely in the frontend without new Go bindings.

**Architecture:** `TerminalView` listens for `contextmenu`, prevents the default WebView menu, and renders a controlled `TerminalContextMenu` component at the cursor position. Copy uses `term.getSelection()` + `navigator.clipboard.writeText`; Paste uses `navigator.clipboard.readText()` + `term.paste()` (which flows through the existing `onData` → `sessions.write` channel); Clear Screen calls `term.clear()` + `term.clearSelection()`. All styling reuses existing CSS variables so the menu matches the active theme.

**Tech Stack:** React 19, xterm.js v6, Vitest + Testing Library (jsdom), Wails WebView2/WebKit clipboard APIs.

## Global Constraints

- 纯前端实现，不新增 Go 绑定或后端 API；保持 Wails-only。
- 不改变现有终端布局、滚轮缩放（Ctrl+wheel）、拖选复制、性能边界（12ms/48KiB 合并、1000 行 scrollback）。
- 菜单视觉复用现有 CSS token（`var(--...)`），三平台行为一致；不改主题/布局/文案体系。
- 清屏为本地视口清空（`term.clear()`），不向远端发送转义序列（避免改变 shell 状态）。
- 测试环境为 jsdom；`@xterm/xterm` 用现有 MockTerminal 双份（`tests/ui/connect-flow.test.tsx` 的 `vi.mock`），需补充 `getSelection/clear/clearSelection/paste` 方法。
- 不提交代码；只修改隔离工作树 `.worktrees\wails-rebuild`。

---

### Task 1: TerminalContextMenu component

**Covers:** 设计 S2（受控菜单组件）

**Files:**
- Create: `src/renderer/src/components/TerminalContextMenu.tsx`
- Create: `tests/ui/terminal-context-menu.test.tsx`
- Modify: `tests/ui/helpers.tsx` (add `navigator.clipboard` stub if not present)

**Interfaces:**
- Consumes: nothing external; `x`/`y` viewport coordinates and action callbacks from `TerminalView` (Task 2).
- Produces: `TerminalContextMenu` with props:
  ```ts
  interface TerminalContextMenuProps {
    x: number
    y: number
    canCopy: boolean
    onCopy: () => void
    onPaste: () => void
    onClear: () => void
    onClose: () => void
  }
  ```
  Renders `role="menu"` with three `role="menuitem"` buttons (Copy disabled when `!canCopy`), positioned with `position: fixed; left: x; top: y`, clamped to the viewport (`Math.min(x, innerWidth - menuWidth)`, `Math.min(y, innerHeight - menuHeight)`).

- [ ] **Step 1: Write the failing test**

Create `tests/ui/terminal-context-menu.test.tsx` (jsdom env):

```tsx
// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { TerminalContextMenu } from '../../src/renderer/src/components/TerminalContextMenu'

describe('TerminalContextMenu', () => {
  it('renders Copy, Paste, and Clear Screen items', () => {
    render(
      <TerminalContextMenu
        x={10}
        y={20}
        canCopy={true}
        onCopy={vi.fn()}
        onPaste={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByRole('menu')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Paste' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Clear Screen' })).toBeInTheDocument()
  })

  it('disables Copy when there is no selection', () => {
    render(
      <TerminalContextMenu
        x={0}
        y={0}
        canCopy={false}
        onCopy={vi.fn()}
        onPaste={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeDisabled()
  })

  it('invokes the action callbacks', () => {
    const onCopy = vi.fn()
    const onPaste = vi.fn()
    const onClear = vi.fn()
    render(
      <TerminalContextMenu
        x={0}
        y={0}
        canCopy={true}
        onCopy={onCopy}
        onPaste={onPaste}
        onClear={onClear}
        onClose={vi.fn()}
      />
    )
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Paste' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Clear Screen' }))
    expect(onCopy).toHaveBeenCalledTimes(1)
    expect(onPaste).toHaveBeenCalledTimes(1)
    expect(onClear).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `node node_modules/vitest/vitest.mjs run tests/ui/terminal-context-menu.test.tsx`
Expected: FAIL — `TerminalContextMenu` not found / module missing.

- [ ] **Step 3: Implement the component**

Create `src/renderer/src/components/TerminalContextMenu.tsx`:

```tsx
import type { ReactNode } from 'react'

export interface TerminalContextMenuProps {
  x: number
  y: number
  canCopy: boolean
  onCopy: () => void
  onPaste: () => void
  onClear: () => void
  onClose: () => void
}

const MENU_WIDTH = 160
const MENU_HEIGHT = 96

export function TerminalContextMenu({
  x,
  y,
  canCopy,
  onCopy,
  onPaste,
  onClear,
  onClose
}: TerminalContextMenuProps): ReactNode {
  const left = Math.min(x, Math.max(0, window.innerWidth - MENU_WIDTH))
  const top = Math.min(y, Math.max(0, window.innerHeight - MENU_HEIGHT))
  return (
    <div
      role="menu"
      className="terminal-context-menu"
      style={{ left, top }}
      onContextMenu={(e) => e.preventDefault()}
      data-testid="terminal-context-menu"
    >
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        disabled={!canCopy}
        onClick={() => {
          onCopy()
          onClose()
        }}
      >
        Copy
      </button>
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        onClick={() => {
          onPaste()
          onClose()
        }}
      >
        Paste
      </button>
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        onClick={() => {
          onClear()
          onClose()
        }}
      >
        Clear Screen
      </button>
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `node node_modules/vitest/vitest.mjs run tests/ui/terminal-context-menu.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/renderer/src/components/TerminalContextMenu.tsx tests/ui/terminal-context-menu.test.tsx
git commit -m "feat: add terminal context menu component with copy/paste/clear"
```

---

### Task 2: Wire contextmenu events and clipboard actions into TerminalView

**Covers:** 设计 S2/S3（事件接线、剪贴板、清屏行为）

**Files:**
- Modify: `src/renderer/src/components/TerminalView.tsx`
- Modify: `tests/ui/connect-flow.test.tsx` (extend `MockTerminal` with `getSelection/clear/clearSelection/paste`)
- Create: `tests/ui/terminal-view-contextmenu.test.tsx`
- Modify: `tests/ui/helpers.tsx` (add `navigator.clipboard` stub in `installFakeApi` or setup)

**Interfaces:**
- Consumes: `TerminalContextMenu` from Task 1; existing `termRef`, `containerRef`, `sessionId`, `registerDataListener`.
- Produces: right-click menu with working Copy/Paste/Clear wired to the real terminal instance.

- [ ] **Step 1: Write the failing test**

Create `tests/ui/terminal-view-contextmenu.test.tsx`. Extend the existing `MockTerminal` (from `connect-flow.test.tsx`) to record `getSelection`, `clear`, `clearSelection`, and `paste`:

```tsx
// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { TerminalView } from '../../src/renderer/src/components/TerminalView'
import { installFakeApi } from './helpers'

vi.mock('@xterm/xterm', () => {
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown> = {}
    selection = ''
    loadAddon = vi.fn()
    open = vi.fn()
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn(() => ({ dispose: vi.fn() }))
    getSelection = vi.fn(() => this.selection)
    clear = vi.fn()
    clearSelection = vi.fn()
    paste = vi.fn()
  }
  return { Terminal: MockTerminal }
})
vi.mock('@xterm/addon-fit', () => {
  class MockFitAddon {
    fit = vi.fn()
  }
  return { FitAddon: MockFitAddon }
})

describe('TerminalView context menu', () => {
  function renderView(): { container: HTMLElement } {
    const api = installFakeApi()
    const { container } = render(
      <TerminalView
        sessionId="s1"
        registerDataListener={() => () => undefined}
        visible={true}
        fontFamily="Hack"
        fontSize={14}
        resolvedTheme="dark"
      />
    )
    return { container }
  }

  it('opens the menu on right-click with the terminal selection state', async () => {
    const { container } = renderView()
    const term = (await import('@xterm/xterm')).Terminal as unknown as {
      getSelection: ReturnType<typeof vi.fn>
    }
    term.getSelection.mockReturnValue('selected text')
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    expect(await screen.findByRole('menu')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeEnabled()
  })

  it('Copy writes the selection to the clipboard', async () => {
    const { container } = renderView()
    const term = (await import('@xterm/xterm')).Terminal as unknown as {
      getSelection: ReturnType<typeof vi.fn>
      clearSelection: ReturnType<typeof vi.fn>
    }
    term.getSelection.mockReturnValue('hello')
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText, readText: vi.fn().mockResolvedValue('') },
      configurable: true
    })
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('hello'))
    expect(term.clearSelection).toHaveBeenCalled()
  })

  it('Paste reads the clipboard and sends it to the terminal', async () => {
    const { container } = renderView()
    const term = (await import('@xterm/xterm')).Terminal as unknown as {
      paste: ReturnType<typeof vi.fn>
    }
    const readText = vi.fn().mockResolvedValue('pasted text')
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(), readText },
      configurable: true
    })
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Paste' }))
    await waitFor(() => expect(readText).toHaveBeenCalled())
    await waitFor(() => expect(term.paste).toHaveBeenCalledWith('pasted text'))
  })

  it('Clear Screen clears the terminal viewport', async () => {
    const { container } = renderView()
    const term = (await import('@xterm/xterm')).Terminal as unknown as {
      clear: ReturnType<typeof vi.fn>
      clearSelection: ReturnType<typeof vi.fn>
    }
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Clear Screen' }))
    expect(term.clear).toHaveBeenCalled()
    expect(term.clearSelection).toHaveBeenCalled()
  })

  it('closes the menu on outside click', async () => {
    const { container } = renderView()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `node node_modules/vitest/vitest.mjs run tests/ui/terminal-view-contextmenu.test.tsx`
Expected: FAIL — no context menu rendered (TerminalView has no contextmenu handling yet).

- [ ] **Step 3: Implement the wiring in TerminalView**

In `TerminalView.tsx`:

1. Add state: `const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)` and `const [hasSelection, setHasSelection] = useState(false)`.
2. Add a `useEffect` (mounted once, alongside the terminal init effect) that:
   - listens `contextmenu` on `containerRef.current`: `e.preventDefault()`, `setMenu({ x: e.clientX, y: e.clientY })`, `setHasSelection(termRef.current ? termRef.current.getSelection().length > 0 : false)`.
   - listens `mousedown` on `window`: `setMenu(null)` when the click is outside the menu element (check `(e.target as HTMLElement).closest('[data-testid="terminal-context-menu"]')`).
   - listens `keydown` on `window`: `Escape` closes the menu.
   - cleanup removes listeners.
3. Action handlers:
   - `handleCopy`: `const sel = termRef.current?.getSelection(); if (sel) { void navigator.clipboard.writeText(sel); termRef.current?.clearSelection(); }`
   - `handlePaste`: `navigator.clipboard.readText().then((text) => termRef.current?.paste(text)).catch(() => { /* clipboard read denied */ })` — and a fallback `document.execCommand('paste')` if `navigator.clipboard?.readText` is unavailable.
   - `handleClear`: `termRef.current?.clear(); termRef.current?.clearSelection();`
4. Render `{menu && <TerminalContextMenu x={menu.x} y={menu.y} canCopy={hasSelection} onCopy={handleCopy} onPaste={handlePaste} onClear={handleClear} onClose={() => setMenu(null)} />}` inside the container div.

- [ ] **Step 4: Run the test to verify it passes**

Run: `node node_modules/vitest/vitest.mjs run tests/ui/terminal-view-contextmenu.test.tsx`
Expected: PASS.

- [ ] **Step 5: Run the full existing UI suite to confirm no regressions**

Run: `node node_modules/vitest/vitest.mjs run tests/ui`
Expected: all existing UI tests still pass (connect-flow, host-form, settings, sftp).

- [ ] **Step 6: Commit**

```bash
git add src/renderer/src/components/TerminalView.tsx tests/ui/terminal-view-contextmenu.test.tsx tests/ui/connect-flow.test.tsx
git commit -m "feat: wire right-click copy/paste/clear into the terminal"
```

---

### Task 3: Menu styling, i18n, and full verification

**Covers:** 设计 S2（样式复用 CSS token）/S4（全量验证）

**Files:**
- Modify: `src/renderer/src/App.css` (or the theme stylesheet used by components)
- Modify: `src/renderer/src/i18n/locales/en.json`
- Modify: `src/renderer/src/i18n/locales/zh.json`

**Interfaces:**
- Consumes: menu component + wiring from Tasks 1–2.
- Produces: theme-matched menu styling and localized labels; full green verification.

- [ ] **Step 1: Add menu styles using existing CSS variables**

Append to `src/renderer/src/App.css`:

```css
.terminal-context-menu {
  position: fixed;
  z-index: 1000;
  min-width: 160px;
  padding: 4px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--bg-elevated);
  box-shadow: var(--shadow-modal);
  display: flex;
  flex-direction: column;
}

.terminal-context-menu-item {
  display: block;
  width: 100%;
  padding: 6px 12px;
  text-align: left;
  border: none;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  cursor: pointer;
  border-radius: 4px;
}

.terminal-context-menu-item:hover:not(:disabled) {
  background: var(--accent-primary);
  color: var(--text-on-accent);
}

.terminal-context-menu-item:disabled {
  color: var(--text-muted);
  cursor: default;
}
```

- [ ] **Step 2: Localize the menu labels**

In `en.json` add under a `terminal` section:
```json
"terminal": {
  "copy": "Copy",
  "paste": "Paste",
  "clearScreen": "Clear Screen"
}
```
In `zh.json`:
```json
"terminal": {
  "copy": "复制",
  "paste": "粘贴",
  "clearScreen": "清屏"
}
```
Update `TerminalContextMenu.tsx` and `TerminalView` action labels to use `t('terminal.copy')` etc. (wrap with `useTranslation`; the test assertions in Tasks 1–2 use the English labels, which stay correct under the test i18n provider's `en.json`).

- [ ] **Step 3: Run the full frontend suite**

Run: `node node_modules/vitest/vitest.mjs run`
Expected: all tests pass (menu component + wiring + existing suites).

- [ ] **Step 4: Run typecheck, lint, and build**

```powershell
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.web.json --composite false
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.test.json
node node_modules/eslint/bin/eslint.js . --no-cache --quiet
node node_modules/vite/bin/vite.js build
```
Expected: all exit 0.

- [ ] **Step 5: Commit**

```bash
git add src/renderer/src/App.css src/renderer/src/i18n/locales/en.json src/renderer/src/i18n/locales/zh.json src/renderer/src/components/TerminalContextMenu.tsx
git commit -m "feat: style and localize the terminal context menu"
```
