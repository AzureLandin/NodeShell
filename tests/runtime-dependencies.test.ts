import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * Runtime-dependency guard (T1.8.2): the shipped product is Wails-only.
 * package.json must not declare any Electron / Node-sidecar / MCP relay
 * dependency, entry point, or build script. This test reads the package
 * manifest (and the lockfile top-level direct sets) relative to the repo
 * root — no transitive tree scanning, so the testing toolchain itself
 * (which may pull in zod etc.) cannot cause false positives.
 */

const repoRoot = new URL('../', import.meta.url)

const forbiddenDependencies = [
  'electron',
  'electron-builder',
  'electron-vite',
  'ssh2',
  '@types/ssh2',
  '@modelcontextprotocol/sdk',
  'font-list',
  'zod',
  '@electron-toolkit/preload',
  '@electron-toolkit/utils',
  '@electron-toolkit/eslint-config-prettier',
  '@electron-toolkit/eslint-config-ts',
  '@electron-toolkit/tsconfig'
]

const forbiddenScripts = [
  'start',
  'dev',
  'build',
  'postinstall',
  'build:unpack',
  'build:win',
  'build:mac',
  'build:mac:x64',
  'build:linux',
  'build:linux:pacman',
  'mcp'
]

const requiredScripts = ['build:wails', 'dev:wails', 'test', 'typecheck', 'typecheck:test']

function readJson<T>(relativePath: string): T {
  return JSON.parse(readFileSync(new URL(relativePath, repoRoot), 'utf8')) as T
}

function directSets(): {
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
} {
  const manifest = readJson<{
    dependencies?: Record<string, string>
    devDependencies?: Record<string, string>
  }>('package.json')
  return {
    dependencies: manifest.dependencies ?? {},
    devDependencies: manifest.devDependencies ?? {}
  }
}

describe('runtime dependencies are Wails-only', () => {
  const { dependencies, devDependencies } = directSets()
  const declared = { ...dependencies, ...devDependencies }

  for (const name of forbiddenDependencies) {
    it(`does not declare forbidden dependency "${name}"`, () => {
      expect(Object.keys(declared)).not.toContain(name)
    })
  }

  it('declares no "main" entry point (Electron main process) and no top-level main field', () => {
    const manifest = readJson<Record<string, unknown>>('package.json')
    expect(manifest.main).toBeUndefined()
  })

  it('does not retain electron-toolkit lockfile top-level direct sets', () => {
    const lock = readJson<{
      packages: Record<
        string,
        {
          dependencies?: Record<string, string>
          devDependencies?: Record<string, string>
        }
      >
    }>('package-lock.json')
    const root = lock.packages[''] ?? {}
    const direct = { ...(root.dependencies ?? {}), ...(root.devDependencies ?? {}) }
    const present = forbiddenDependencies.filter((name) => name in direct)
    expect(present).toEqual([])
  })
})

describe('scripts are Wails-only', () => {
  const manifest = readJson<{ scripts?: Record<string, string> }>('package.json')
  const scripts = manifest.scripts ?? {}

  for (const name of forbiddenScripts) {
    it(`does not define old script "${name}"`, () => {
      expect(Object.keys(scripts)).not.toContain(name)
    })
  }

  for (const name of requiredScripts) {
    it(`keeps required script "${name}"`, () => {
      expect(Object.keys(scripts)).toContain(name)
    })
  }
})
