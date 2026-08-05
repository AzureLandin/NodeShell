// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { isEditableTextFile, MAX_EDITABLE_TEXT_BYTES } from '../src/shared/editable-text'

describe('isEditableTextFile', () => {
  it('accepts common text and source extensions (case-insensitive)', () => {
    expect(isEditableTextFile('notes.txt')).toBe(true)
    expect(isEditableTextFile('Config.JSON')).toBe(true)
    expect(isEditableTextFile('app.toml')).toBe(true)
    expect(isEditableTextFile('main.py')).toBe(true)
    expect(isEditableTextFile('util.c')).toBe(true)
    expect(isEditableTextFile('.env')).toBe(true)
    expect(isEditableTextFile('.gitignore')).toBe(true)
  })

  it('rejects binaries and extensionless names', () => {
    expect(isEditableTextFile('photo.png')).toBe(false)
    expect(isEditableTextFile('archive.zip')).toBe(false)
    expect(isEditableTextFile('Makefile')).toBe(false)
    expect(isEditableTextFile('README')).toBe(false)
  })

  it('uses the basename when a path is passed', () => {
    expect(isEditableTextFile('/home/u/app.go')).toBe(true)
    expect(isEditableTextFile('/home/u/bin')).toBe(false)
  })

  it('exports the shared 512KiB cap', () => {
    expect(MAX_EDITABLE_TEXT_BYTES).toBe(512 * 1024)
  })
})
