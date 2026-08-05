import { useEffect, useRef } from 'react'
import { EditorView, basicSetup } from 'codemirror'
import { Compartment, EditorState } from '@codemirror/state'
import { keymap } from '@codemirror/view'
import { indentWithTab } from '@codemirror/commands'
import { oneDark } from '@codemirror/theme-one-dark'
import { languageExtensionForFile } from '../sftp-editor-language'

export interface SftpCodeEditorProps {
  /** Initial document; remount (via key) when opening another file. */
  initialValue: string
  /** Remote basename — drives syntax highlighting. */
  filename: string
  readOnly?: boolean
  'aria-label'?: string
  onChange: (value: string) => void
  onReady?: (view: EditorView) => void
}

function isDarkTheme(): boolean {
  return document.documentElement.getAttribute('data-theme') !== 'light'
}

/** Light theme aligned with NodeShell tokens (oneDark covers dark mode). */
const lightTheme = EditorView.theme(
  {
    '&': {
      color: 'var(--text-primary)',
      backgroundColor: 'var(--bg-elevated)'
    },
    '.cm-content': {
      caretColor: 'var(--text-bright)'
    },
    '&.cm-focused .cm-cursor': {
      borderLeftColor: 'var(--text-bright)'
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: 'rgba(58, 110, 168, 0.28)'
    },
    '.cm-gutters': {
      backgroundColor: 'var(--bg-panel)',
      color: 'var(--text-muted)',
      border: 'none',
      borderRight: '1px solid var(--border-subtle)'
    },
    '.cm-activeLineGutter': {
      backgroundColor: 'var(--bg-hover)'
    },
    '.cm-activeLine': {
      backgroundColor: 'var(--bg-hover)'
    }
  },
  { dark: false }
)

const editorChrome = EditorView.theme({
  '&': {
    height: '100%',
    fontSize: '13px'
  },
  '.cm-scroller': {
    fontFamily: "Consolas, 'Cascadia Mono', 'Courier New', monospace",
    lineHeight: '1.5',
    overflow: 'auto'
  },
  '.cm-content': {
    padding: '10px 0'
  },
  '.cm-gutters': {
    paddingRight: '4px'
  }
})

/**
 * CodeMirror 6 host for the SFTP text editor. Owns the EditorView for the
 * lifetime of the mount; parent should remount (key=path) when switching files.
 */
export function SftpCodeEditor({
  initialValue,
  filename,
  readOnly = false,
  'aria-label': ariaLabel,
  onChange,
  onReady
}: SftpCodeEditorProps): React.JSX.Element {
  const parentRef = useRef<HTMLDivElement | null>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const themeCompartment = useRef(new Compartment())
  const editableCompartment = useRef(new Compartment())

  useEffect(() => {
    const parent = parentRef.current
    if (!parent) return

    const language = languageExtensionForFile(filename)
    const view = new EditorView({
      parent,
      state: EditorState.create({
        doc: initialValue,
        extensions: [
          basicSetup,
          keymap.of([indentWithTab]),
          editorChrome,
          themeCompartment.current.of(isDarkTheme() ? oneDark : lightTheme),
          editableCompartment.current.of([
            EditorState.readOnly.of(readOnly),
            EditorView.editable.of(!readOnly)
          ]),
          ...(language ? [language] : []),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              onChangeRef.current(update.state.doc.toString())
            }
          }),
          EditorView.contentAttributes.of({
            'aria-label': ariaLabel ?? filename
          })
        ]
      })
    })
    viewRef.current = view
    onReady?.(view)
    view.focus()

    const observer = new MutationObserver(() => {
      view.dispatch({
        effects: themeCompartment.current.reconfigure(isDarkTheme() ? oneDark : lightTheme)
      })
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme']
    })

    return () => {
      observer.disconnect()
      view.destroy()
      viewRef.current = null
    }
    // Mount once per file open (parent remounts via key).
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentional
  }, [])

  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: editableCompartment.current.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly)
      ])
    })
  }, [readOnly])

  return <div ref={parentRef} className="sftp-code-editor" />
}
