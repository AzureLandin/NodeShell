import type { MouseEvent } from 'react'
import Markdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

/**
 * Sidebar markdown for assistant replies. Raw HTML is dropped (react-markdown
 * never uses innerHTML, and skipHtml keeps tags out of the tree). Images are
 * not fetched — a remote src would let the model pull the WebView onto an
 * arbitrary host. Links are http(s) only and open in the system browser
 * through AppOpenExternal, so a click cannot navigate the app itself.
 */

/** Only http(s) with a host; javascript:/data:/file: and relative paths are out. */
export function safeHttpUrl(href: string | undefined): string | null {
  if (!href) return null
  try {
    const url = new URL(href)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    if (!url.hostname) return null
    return url.href
  } catch {
    return null
  }
}

function urlTransform(value: string): string {
  return safeHttpUrl(value) ?? ''
}

function openExternal(href: string): void {
  const open = window.api.app.openExternal
  if (open) void open(href)
}

function handleLinkClick(event: MouseEvent<HTMLAnchorElement>, href: string): void {
  event.preventDefault()
  openExternal(href)
}

const components: Components = {
  a({ href, children }) {
    const url = safeHttpUrl(href)
    if (!url) return <span>{children}</span>
    return (
      <a href={url} onClick={(event) => handleLinkClick(event, url)}>
        {children}
      </a>
    )
  },
  // GFM task-list checkboxes are display-only; any other input is dropped.
  input({ type, checked }) {
    if (type !== 'checkbox') return null
    return <input type="checkbox" checked={Boolean(checked)} disabled readOnly />
  }
}

export function AgentMarkdown({ text }: { text: string }): React.JSX.Element {
  return (
    <div className="agent-md">
      <Markdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        urlTransform={urlTransform}
        disallowedElements={['img', 'iframe', 'video', 'audio', 'object', 'embed', 'form']}
        components={components}
      >
        {text}
      </Markdown>
    </div>
  )
}
