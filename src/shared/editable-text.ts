/** Hard cap shared with MCP sftp_read/sftp_write and the Go GUI bindings. */
export const MAX_EDITABLE_TEXT_BYTES = 512 * 1024

/**
 * Extensions treated as editable text in the SFTP browser (lowercase, with
 * leading dot). Keep this list conservative — binary-looking names still fall
 * through to Download.
 */
const EDITABLE_EXTENSIONS = new Set([
  '.txt',
  '.md',
  '.markdown',
  '.json',
  '.toml',
  '.yaml',
  '.yml',
  '.xml',
  '.html',
  '.htm',
  '.css',
  '.scss',
  '.less',
  '.js',
  '.jsx',
  '.mjs',
  '.cjs',
  '.ts',
  '.tsx',
  '.py',
  '.rb',
  '.go',
  '.rs',
  '.c',
  '.h',
  '.cpp',
  '.cc',
  '.cxx',
  '.hpp',
  '.hxx',
  '.java',
  '.kt',
  '.kts',
  '.cs',
  '.swift',
  '.php',
  '.pl',
  '.pm',
  '.lua',
  '.r',
  '.sql',
  '.sh',
  '.bash',
  '.zsh',
  '.fish',
  '.ps1',
  '.bat',
  '.cmd',
  '.ini',
  '.cfg',
  '.conf',
  '.config',
  '.env',
  '.gitignore',
  '.dockerignore',
  '.editorconfig',
  '.properties',
  '.gradle',
  '.cmake',
  '.makefile',
  '.mk',
  '.log',
  '.csv',
  '.tsv',
  '.svg',
  '.vue',
  '.svelte',
  '.astro'
])

/**
 * Returns true when `name` (a basename, not a path) looks like an editable
 * text file. Matching is by extension only; extensionless names are not
 * editable in the MVP.
 */
export function isEditableTextFile(name: string): boolean {
  const base = name.includes('/') ? name.slice(name.lastIndexOf('/') + 1) : name
  const dot = base.lastIndexOf('.')
  if (dot < 0) return false
  return EDITABLE_EXTENSIONS.has(base.slice(dot).toLowerCase())
}
