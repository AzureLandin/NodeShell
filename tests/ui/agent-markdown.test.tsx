// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AgentMarkdown } from '../../src/renderer/src/components/AgentMarkdown'
import { installFakeApi, renderWithI18n } from './helpers'

/**
 * Assistant markdown: GFM rendering, no raw HTML, no remote images, and
 * http(s) links leave through AppOpenExternal instead of navigating the
 * WebView. javascript:/data: hrefs must not become anchors.
 */

function renderMarkdown(text: string): ReturnType<typeof installFakeApi> {
  const fake = installFakeApi()
  renderWithI18n(<AgentMarkdown text={text} />)
  return fake
}

describe('AgentMarkdown rendering', () => {
  it('renders headings, emphasis, lists and fenced code', () => {
    renderMarkdown(
      [
        '## Disk',
        '',
        'Looks **fine**.',
        '',
        '- `/` is 40%',
        '',
        '```sh',
        'df -h',
        '```'
      ].join('\n')
    )

    expect(screen.getByRole('heading', { level: 2, name: 'Disk' })).toBeInTheDocument()
    expect(screen.getByText('fine').closest('strong')).toBeInTheDocument()
    expect(screen.getByText('/', { exact: false }).closest('li')).toBeInTheDocument()
    expect(screen.getByText('df -h').closest('code')).toBeInTheDocument()
  })

  it('renders a GFM table', () => {
    renderMarkdown(['| fs | use |', '| --- | --- |', '| `/` | 40% |'].join('\n'))

    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'fs' })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '40%' })).toBeInTheDocument()
  })
})

describe('AgentMarkdown safety', () => {
  it('does not turn raw HTML into DOM nodes', () => {
    renderMarkdown('Hello <script>window.__pwned = 1</script> **world**')

    // Tags are dropped; leftover text is fine as long as nothing executes.
    expect(screen.getByText('world').closest('strong')).toBeInTheDocument()
    expect(document.querySelector('script')).toBeNull()
    expect((window as Window & { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('does not fetch or render a remote image', () => {
    renderMarkdown('See ![secret](https://evil.test/pixel.png)')

    expect(document.querySelector('img')).toBeNull()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('drops javascript and data hrefs so they are not clickable', () => {
    renderMarkdown('[x](javascript:alert(1)) [y](data:text/html,hi) [ok](https://example.com/docs)')

    expect(screen.queryByRole('link', { name: 'x' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'y' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'ok' })).toHaveAttribute(
      'href',
      'https://example.com/docs'
    )
  })

  it('opens an http(s) link through AppOpenExternal and does not navigate', async () => {
    const fake = renderMarkdown('See [the host](https://example.com/status)')
    const user = userEvent.setup()

    const link = screen.getByRole('link', { name: 'the host' })
    await user.click(link)

    expect(fake.mocks.app.openExternal).toHaveBeenCalledWith('https://example.com/status')
  })
})
