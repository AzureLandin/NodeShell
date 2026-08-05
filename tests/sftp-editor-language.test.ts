import { describe, expect, it } from 'vitest'
import { languageExtensionForFile } from '../src/renderer/src/sftp-editor-language'

describe('languageExtensionForFile', () => {
  it('returns a highlighter for common source extensions', () => {
    expect(languageExtensionForFile('app.py')).toBeDefined()
    expect(languageExtensionForFile('main.go')).toBeDefined()
    expect(languageExtensionForFile('util.c')).toBeDefined()
    expect(languageExtensionForFile('Config.JSON')).toBeDefined()
    expect(languageExtensionForFile('frpc.toml')).toBeDefined()
    expect(languageExtensionForFile('script.sh')).toBeDefined()
    expect(languageExtensionForFile('notes.md')).toBeDefined()
  })

  it('returns undefined for plain / unknown text', () => {
    expect(languageExtensionForFile('notes.txt')).toBeUndefined()
    expect(languageExtensionForFile('README')).toBeUndefined()
    expect(languageExtensionForFile('.gitignore')).toBeUndefined()
  })
})
