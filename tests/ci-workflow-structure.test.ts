import { readFileSync, existsSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const repoRoot = new URL('../', import.meta.url)

function readYaml(relativePath: string): string {
  const p = new URL(relativePath, repoRoot)
  if (!existsSync(p)) throw new Error(`missing: ${relativePath}`)
  return readFileSync(p, 'utf8')
}

describe('CI workflow structure', () => {
  it('has .github/workflows/ci.yml', () => {
    expect(() => readYaml('.github/workflows/ci.yml')).not.toThrow()
  })

  it('removed stale Electron workflows', () => {
    expect(existsSync(new URL('../.github/workflows/build-mac-arm64.yml', import.meta.url))).toBe(false)
    expect(existsSync(new URL('../.github/workflows/build-linux-pacman.yml', import.meta.url))).toBe(false)
  })

  it('ci.yml targets all three native platforms', () => {
    const yaml = readYaml('.github/workflows/ci.yml')
    expect(yaml).toContain('windows-latest')
    expect(yaml).toContain('macos-14')
    expect(yaml).toContain('ubuntu-latest')
  })

  it('ci.yml runs tests before packaging', () => {
    const yaml = readYaml('.github/workflows/ci.yml')
    const testIdx = yaml.indexOf('npm run test')
    const wailsIdx = yaml.indexOf('wails build')
    expect(testIdx).toBeGreaterThan(-1)
    expect(wailsIdx).toBeGreaterThan(testIdx)
  })

  it('ci.yml uploads per-platform artifacts', () => {
    const yaml = readYaml('.github/workflows/ci.yml')
    expect(yaml).toContain('upload-artifact')
    expect(yaml).toMatch(/NodeShell-[^]*-(windows|macos|linux)/)
  })

  it('keeps both MCP smoke scripts present', () => {
    expect(existsSync(new URL('../scripts/mcp-smoke.ps1', import.meta.url))).toBe(true)
    expect(existsSync(new URL('../scripts/mcp-smoke.sh', import.meta.url))).toBe(true)
  })
})

describe('CI packaging', () => {
  const ci = readYaml('.github/workflows/ci.yml')

  it('Windows job installs NSIS and produces an installer', () => {
    expect(ci).toContain('choco install nsis')
    expect(ci).toMatch(/nsis|\.exe/i)
  })

  it('macOS job produces DMG and ZIP', () => {
    expect(ci).toMatch(/hdiutil|\.dmg/)
    expect(ci).toMatch(/ditto|zip|\.zip/)
  })

  it('Linux job produces AppImage and deb', () => {
    expect(ci).toMatch(/appimagetool|AppImage/)
    expect(ci).toMatch(/nfpm|\.deb/)
  })

  it('keeps signing injection points for Windows and macOS', () => {
    expect(ci).toContain('WINDOWS_SIGN')
    expect(ci).toContain('APPLE_SIGN')
  })
})

describe('release workflow', () => {
  const ci = readYaml('.github/workflows/ci.yml')

  it('has a release job in ci.yml', () => {
    expect(ci).toMatch(/^\s{2}release:/m)
  })

  it('triggers on v* tags', () => {
    expect(ci).toMatch(/tags:\s*\n\s*-\s+['"]?v\*/)
  })

  it('release job writes contents', () => {
    expect(ci).toMatch(/contents:\s*write/)
  })

  it('downloads artifacts and creates a release', () => {
    expect(ci).toContain('download-artifact')
    expect(ci).toMatch(/action-gh-release|gh release create/)
  })
})
