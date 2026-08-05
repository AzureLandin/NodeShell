import { type Extension } from '@codemirror/state'
import { StreamLanguage } from '@codemirror/language'
import { javascript } from '@codemirror/lang-javascript'
import { json } from '@codemirror/lang-json'
import { python } from '@codemirror/lang-python'
import { cpp } from '@codemirror/lang-cpp'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { markdown } from '@codemirror/lang-markdown'
import { xml } from '@codemirror/lang-xml'
import { yaml } from '@codemirror/lang-yaml'
import { sql } from '@codemirror/lang-sql'
import { rust } from '@codemirror/lang-rust'
import { java } from '@codemirror/lang-java'
import { php } from '@codemirror/lang-php'
import { go } from '@codemirror/lang-go'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { powerShell } from '@codemirror/legacy-modes/mode/powershell'
import { properties } from '@codemirror/legacy-modes/mode/properties'
import { ruby } from '@codemirror/legacy-modes/mode/ruby'

/**
 * Returns a CodeMirror language extension for a remote basename, or undefined
 * when we have no dedicated highlighter (plain text still edits fine).
 */
export function languageExtensionForFile(name: string): Extension | undefined {
  const base = name.includes('/') ? name.slice(name.lastIndexOf('/') + 1) : name
  const lower = base.toLowerCase()
  const dot = lower.lastIndexOf('.')
  const ext = dot >= 0 ? lower.slice(dot) : ''

  switch (ext) {
    case '.js':
    case '.mjs':
    case '.cjs':
      return javascript()
    case '.jsx':
      return javascript({ jsx: true })
    case '.ts':
      return javascript({ typescript: true })
    case '.tsx':
      return javascript({ jsx: true, typescript: true })
    case '.json':
      return json()
    case '.py':
      return python()
    case '.c':
    case '.h':
    case '.cpp':
    case '.cc':
    case '.cxx':
    case '.hpp':
    case '.hxx':
      return cpp()
    case '.html':
    case '.htm':
      return html()
    case '.css':
    case '.scss':
    case '.less':
      return css()
    case '.md':
    case '.markdown':
      return markdown()
    case '.xml':
    case '.svg':
      return xml()
    case '.yaml':
    case '.yml':
      return yaml()
    case '.sql':
      return sql()
    case '.rs':
      return rust()
    case '.java':
      return java()
    case '.php':
      return php()
    case '.go':
      return go()
    case '.toml':
      return StreamLanguage.define(toml)
    case '.sh':
    case '.bash':
    case '.zsh':
    case '.fish':
    case '.bat':
    case '.cmd':
      return StreamLanguage.define(shell)
    case '.ps1':
      return StreamLanguage.define(powerShell)
    case '.ini':
    case '.cfg':
    case '.conf':
    case '.config':
    case '.env':
    case '.properties':
    case '.editorconfig':
      return StreamLanguage.define(properties)
    case '.rb':
      return StreamLanguage.define(ruby)
    default:
      return undefined
  }
}
