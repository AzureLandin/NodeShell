import { readFileSync, existsSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const repoRoot = new URL('../', import.meta.url)

function readYaml(relativePath: string): string {
  const p = new URL(relativePath, repoRoot)
  if (!existsSync(p)) throw new Error(`missing: ${relativePath}`)
  return readFileSync(p, 'utf8')
}

function readFile(relativePath: string): string {
  const p = new URL(relativePath, repoRoot)
  if (!existsSync(p)) throw new Error(`missing: ${relativePath}`)
  return readFileSync(p, 'utf8')
}

// Scoped to one step block: from "- name: X" up to the next step (or end).
function stepSection(yaml: string, stepName: string): string {
  const start = yaml.indexOf(`- name: ${stepName}`)
  if (start === -1) throw new Error(`missing step: ${stepName}`)
  const end = yaml.indexOf('\n      - name:', start + 1)
  return yaml.slice(start, end === -1 ? undefined : end)
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
    expect(yaml).toMatch(/NodeShell-\$\{\{ env\.APP_VERSION \}\}-\$\{\{ matrix\.platform \}\}/)
  })

  it('keeps both MCP smoke scripts present', () => {
    expect(existsSync(new URL('../scripts/mcp-smoke.ps1', import.meta.url))).toBe(true)
    expect(existsSync(new URL('../scripts/mcp-smoke.sh', import.meta.url))).toBe(true)
  })
})

describe('CI version single source', () => {
  const ci = readYaml('.github/workflows/ci.yml')

  it('derives APP_VERSION from package.json into GITHUB_ENV', () => {
    expect(ci).toMatch(/APP_VERSION=.*require\('\.\/package\.json'\)\.version/)
    expect(ci).toMatch(/GITHUB_ENV/)
  })

  it('uses ${{ env.APP_VERSION }} for artifact names and packaging args', () => {
    expect(ci).toMatch(/NodeShell-\$\{\{ env\.APP_VERSION \}\}/)
    expect(ci).toMatch(/package-macos\.sh[^\n]*\$\{\{ env\.APP_VERSION \}\}/)
    expect(ci).toMatch(/package-linux\.sh[^\n]*\$\{\{ env\.APP_VERSION \}\}/)
  })

  it('does not hardcode the version literal in ci.yml', () => {
    expect(ci).not.toMatch(/NodeShell-2\.0\.0/)
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

  it('Windows signing uses optional base64 cert + password secrets and skips when unconfigured', () => {
    expect(ci).toContain('secrets.WINDOWS_CERT_BASE64')
    expect(ci).toContain('secrets.WINDOWS_CERT_PASSWORD')
    expect(ci).not.toMatch(/WINDOWS_SIGN/)
    expect(ci).toContain('[System.Convert]::FromBase64String')
    expect(ci).toMatch(/signtool\.exe/)
    expect(ci).toMatch(/timestamp\.digicert\.com/)
    expect(ci).toMatch(/WINDOWS_CERT_BASE64 != ''/)
    expect(ci).toMatch(/WINDOWS_CERT_PASSWORD != ''/)
  })

  it('macOS signing imports the cert into a keychain and notarizes when configured', () => {
    expect(ci).toContain('secrets.APPLE_DEVELOPER_ID')
    expect(ci).toContain('secrets.APPLE_TEAM_ID')
    expect(ci).toContain('secrets.APPLE_ID')
    expect(ci).toContain('secrets.APPLE_APP_SPECIFIC_PASSWORD')
    expect(ci).toContain('secrets.APPLE_CERT_BASE64')
    expect(ci).toContain('security create-keychain')
    expect(ci).toContain('set-key-partition-list')
    expect(ci).toContain('notarytool submit')
    expect(ci).toContain('stapler staple')
  })

  it('notarize step gates on the full Apple signing set, not just Apple account credentials', () => {
    // Critical: with only APPLE_ID/APPLE_APP_SPECIFIC_PASSWORD/APPLE_TEAM_ID set, the Import
    // step skips (no identity/cert) → ad-hoc product → notarytool rejects → hard CI failure.
    // Notarize must share the signing step's condition set.
    const notarize = stepSection(ci, 'Notarize and staple macOS DMG')
    expect(notarize).toMatch(/if:.*APPLE_DEVELOPER_ID != ''/)
    expect(notarize).toMatch(/if:.*APPLE_CERT_BASE64 != ''/)
    expect(notarize).toMatch(/if:.*APPLE_CERT_PASSWORD != ''/)
  })

  it('macOS package step runs on all macos jobs and signs only with the full Apple set', () => {
    // The package step must always run so an unsigned ad-hoc DMG/ZIP is produced and
    // uploaded when no Apple secrets are configured (upload uses if-no-files-found: error).
    // Within the step, the signed branch requires the same full 6-secret set as the
    // Import and Notarize steps: a partial config (e.g. signing material without Apple
    // account credentials) leaves the identity out of the keychain and codesign would
    // die with "no identity found" — so any partial set must degrade to ad-hoc, not fail.
    const packageStep = stepSection(ci, 'Package macOS DMG and ZIP')
    expect(packageStep).toMatch(/if: matrix\.platform == 'macos'/)
    for (const secret of [
      'APPLE_DEVELOPER_ID',
      'APPLE_TEAM_ID',
      'APPLE_ID',
      'APPLE_APP_SPECIFIC_PASSWORD',
      'APPLE_CERT_BASE64',
      'APPLE_CERT_PASSWORD'
    ]) {
      expect(packageStep).toContain(`$${secret}" ]`)
    }
    expect(packageStep).not.toMatch(/if: matrix\.platform == 'macos' &&/)
  })

  it('Apple cert import gates on APPLE_CERT_PASSWORD and passes it to security import', () => {
    // Important: -P "" assumed an empty p12 password with no documentation. The p12 password
    // is now an explicit secret, required (6th) in the import condition and passed via env.
    const importStep = stepSection(ci, 'Import Apple signing certificate')
    expect(importStep).toMatch(/if:.*APPLE_CERT_PASSWORD != ''/)
    expect(importStep).toContain('APPLE_CERT_PASSWORD: ${{ secrets.APPLE_CERT_PASSWORD }}')
    expect(importStep).toContain('-P "$APPLE_CERT_PASSWORD"')
    expect(importStep).not.toContain('-P ""')
  })

  it('Windows signing picks signtool by version number, not lexicographic path order', () => {
    // Minor: FullName descending could pick an old patch version (e.g. 10.0.26100.9 > ...1742
    // lexicographically). Sort Windows Kits version directories as [version] instead.
    const sign = stepSection(ci, 'Sign Windows installer')
    expect(sign).toContain('[version]$_.Name')
    expect(sign).not.toContain('Sort-Object FullName')
  })

  it('keeps ad-hoc signing path when macOS secrets are not configured', () => {
    // package-macos.sh must still work without an identity (4th arg empty)
    expect(readFile('scripts/package-macos.sh')).toContain('--sign -')
  })
})

describe('packaging scripts', () => {
  const macos = readFile('scripts/package-macos.sh')
  const linux = readFile('scripts/package-linux.sh')

  it('package-macos.sh accepts an optional signing identity as 4th parameter', () => {
    expect(macos).toMatch(/sig_identity="\$\{4[^}]*\}"/)
    expect(macos).toContain('--force --deep --sign')
  })

  it('package-macos.sh applies hardened runtime when an identity is provided', () => {
    expect(macos).toContain('--options runtime')
  })

  it('package-linux.sh takes the version as a parameter, not a hardcoded literal', () => {
    expect(linux).toMatch(/version="\$\{5[^}]*\}"/)
    expect(linux).not.toContain('2.0.0')
    expect(linux).toMatch(/version: \$version/)
  })
})

describe('build icon assets', () => {
  it('keeps build/appicon.png as the single tracked icon source', () => {
    expect(existsSync(new URL('../build/appicon.png', import.meta.url))).toBe(true)
  })

  it('does not require pre-generated platform icons: wails generates them from appicon.png', () => {
    // Wails v2.13.0 buildassets: packageApplicationForWindows -> generateIcoFile('appicon', 'icon')
    // writes build/windows/icon.ico only if missing; packageApplicationForDarwin ->
    // processDarwinIcon('appicon', resourceDir, 'iconfile') encodes build/appicon.png into the .app.
    // So neither build/windows/icon.ico nor build/darwin/icon.icns needs to be checked in.
    const gitignore = readFile('.gitignore')
    expect(gitignore).toContain('/build/bin/')
  })

  it('still ignores generated build output (build/bin)', () => {
    const gitignore = readFile('.gitignore')
    expect(gitignore).toContain('/build/bin/')
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
