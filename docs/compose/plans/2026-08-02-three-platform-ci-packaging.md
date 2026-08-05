# Three-Platform CI, Packaging & Install Smoke Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use compose:subagent to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the stale Electron CI workflows with three-native-runner Wails build/packaging pipelines that run the full test suite, build platform products, smoke-test them, and publish on tag.

**Architecture:** A single reusable workflow (`ci.yml`) drives a per-platform matrix (windows/amd64, darwin/arm64, linux/amd64) on native runners. Each job installs Wails CLI + platform deps, runs Go/TS tests, types, lint, Vite build, `wails build`, packaging, and an `--mcp` stdio handshake smoke. A separate `release.yml` aggregates artifacts on `v*` tags and creates a GitHub Release with optional signing injection points. Linux packaging beyond the bare binary (AppImage/deb) uses a post-build nfpm/appimagetool step because Wails v2.13's Linux packager is a no-op; macOS DMG/ZIP are produced with `hdiutil`/`ditto`; Windows NSIS uses `choco install nsis`.

**Tech Stack:** Go 1.26.x, Wails CLI v2.13, Node 22, Vite 7, Vitest 4, TypeScript 5.9, GitHub Actions (setup-go, setup-node, upload/download-artifact, action-gh-release).

## Global Constraints

- 构建必须在对应原生 CI runner 上进行（禁止交叉编译 macOS）。
- 未配置签名材料时允许生成未签名测试产物，但发布流程须保留 Windows 签名与 macOS 签名/公证的注入点。
- 成品不得依赖 Node.js 运行时或旧 Electron MCP relay；CI 仅可安装 Node 用于前端 Vite 构建。
- 产物命名沿用 `NodeShell-{version}-{platform}-{arch}`。
- 不修改本机 Go 绝对路径；CI 一律用 `setup-go` 与 PATH 中的 `wails`。
- 不提交代码；只修改隔离工作树 `.worktrees\wails-rebuild`。

---

### Task 1: Reusable three-platform CI workflow with tests, Wails build, and --mcp smoke

**Covers:** S5.2

**Files:**
- Create: `.github/workflows/ci.yml`
- Delete: `.github/workflows/build-mac-arm64.yml`
- Delete: `.github/workflows/build-linux-pacman.yml`
- Create: `scripts/mcp-smoke.ps1` (cross-platform handshake; pure stdio + exit-code asserts)
- Create: `scripts/mcp-smoke.sh` (POSIX equivalent for mac/linux runners)
- Create: `tests/ci-workflow-structure.test.ts` (RED guard: ci.yml exists, stale workflows absent, required jobs present)

**Interfaces:**
- Consumes: `package.json` scripts (`typecheck`, `test`, `lint`, `build:wails`); `wails.json`; Go test suite.
- Produces: `NodeShell-{version}-{platform}-{arch}` artifacts uploaded as named workflow artifacts, available to the release workflow in Task 3.

- [ ] **Step 1: Write the RED guard test**

Create `tests/ci-workflow-structure.test.ts`:

```ts
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
})
```

- [ ] **Step 2: Run the guard test to verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: FAIL — ci.yml absent, stale workflows still present.

- [ ] **Step 3: Delete the stale Electron workflows**

Delete `.github/workflows/build-mac-arm64.yml` and `.github/workflows/build-linux-pacman.yml` (they call deleted `npm run build` and `electron-builder`).

- [ ] **Step 4: Create the cross-platform MCP smoke scripts**

`scripts/mcp-smoke.ps1` (Windows runners):

```powershell
$ErrorActionPreference = 'Stop'
$exe = $args[0]
$tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::GetTempPath() + "mcp-smoke-$([guid]::NewGuid())") -Force
$in = Join-Path $tmp 'in.jsonl'
$stdout = Join-Path $tmp 'out.txt'
$stderr = Join-Path $tmp 'err.txt'
@(
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}',
  '{"jsonrpc":"2.0","method":"notifications/initialized"}',
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}',
  '{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}'
) | Set-Content -LiteralPath $in -Encoding utf8
$p = Start-Process -FilePath $exe -ArgumentList '--mcp' -RedirectStandardInput $in -RedirectStandardOutput $stdout -RedirectStandardError $stderr -NoNewWindow -PassThru
if (-not $p.WaitForExit(30000)) { $p.Kill(); throw 'MCP process timed out' }
if ($p.ExitCode -ne 0) { throw "MCP exit $($p.ExitCode): $(Get-Content -Raw $stderr)" }
$err = Get-Content -Raw $stderr
if ($err.Length -ne 0) { throw "MCP stderr not empty: $err" }
$lines = @(Get-Content -LiteralPath $stdout | Where-Object { $_ })
if ($lines.Count -ne 3) { throw "expected 3 responses, got $($lines.Count)" }
$init = $lines[0] | ConvertFrom-Json
if ($init.result.protocolVersion -ne '2024-11-05') { throw "protocol mismatch: $($init.result.protocolVersion)" }
$tools = $lines[1] | ConvertFrom-Json
if (@($tools.result.tools).Count -ne 10) { throw "expected 10 tools, got $(@($tools.result.tools).Count)" }
Remove-Item -Recurse -Force $tmp
Write-Host 'MCP smoke OK'
```

`scripts/mcp-smoke.sh` (mac/linux runners):

```bash
#!/usr/bin/env bash
set -euo pipefail
exe="$1"
tmp="$(mktemp -d)"
in="$tmp/in.jsonl"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}' > "$in"
out="$tmp/out.txt"
err="$tmp/err.txt"
"$exe" --mcp < "$in" > "$out" 2> "$err" &
pid=$!
if ! timeout 30 bash -c "wait $pid"; then kill "$pid" 2>/dev/null || true; echo "MCP process timed out"; exit 1; fi
wait "$pid"
code=$?
if [ "$code" -ne 0 ]; then echo "MCP exit $code: $(cat "$err")"; exit 1; fi
if [ -s "$err" ]; then echo "MCP stderr not empty: $(cat "$err")"; exit 1; fi
lines=$(grep -c . "$out" || true)
if [ "$lines" -ne 3 ]; then echo "expected 3 responses, got $lines"; exit 1; fi
proto=$(sed -n '1p' "$out" | grep -o '"protocolVersion":"[^"]*"')
if [ "$proto" != '"protocolVersion":"2024-11-05"' ]; then echo "protocol mismatch: $proto"; exit 1; fi
tools=$(sed -n '2p' "$out" | grep -o '"tools":\[' | head -1)
if [ -z "$tools" ]; then echo "missing tools array"; exit 1; fi
tool_count=$(sed -n '2p' "$out" | grep -o '"name"' | wc -l | tr -d ' ')
if [ "$tool_count" -ne "10" ]; then echo "expected 10 tools, got $tool_count"; exit 1; fi
rm -rf "$tmp"
echo "MCP smoke OK"
```

- [ ] **Step 5: Create ci.yml with reusable platform matrix**

Create `.github/workflows/ci.yml`. The workflow:

- Triggers on `push` (branches + tags `v*`), `pull_request`, and `workflow_dispatch`.
- `permissions: contents: read`.
- A `build` job runs a matrix over three native runners: `windows-latest`, `macos-14`, `ubuntu-latest`.
- Per-job steps:
  1. `actions/checkout@v4`
  2. `actions/setup-go@v5` with `go-version: '1.26.x'`, `cache: true` (uses `go.sum`)
  3. `actions/setup-node@v4` with `node-version: '22'`, `cache: 'npm'`
  4. `npm ci`
  5. `npm run typecheck`
  6. `npm test`
  7. `npm run lint`
  8. `go vet ./...`
  9. `go test -race -count=1 ./...`
  10. `npm run build:wails` (Vite → `frontend/dist` so go:embed succeeds)
  11. Wails CLI install: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0` (ensures runner has `wails` on PATH; pin to v2.13.0)
  12. Platform dep install for linux: `sudo apt-get update && sudo apt-get install -y gcc pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev`
  13. `wails build -skipbindings` (platform-specific packaging added in Task 2; here just the binary)
  14. MCP smoke: on Windows `powershell -File scripts/mcp-smoke.ps1 build/bin/nodeshell.exe`; on mac/linux `bash scripts/mcp-smoke.sh build/bin/nodeshell`
  15. `actions/upload-artifact@v4` with name `NodeShell-2.0.0-${{ matrix.platform }}` and path `build/bin/nodeshell*`
- `matrix.include`:
  - `{ platform: windows, runner: windows-latest, wails-args: '-platform windows/amd64', smoke-cmd: 'powershell -File scripts/mcp-smoke.ps1', smoke-target: 'build/bin/nodeshell.exe' }`
  - `{ platform: macos, runner: macos-14, wails-args: '-platform darwin/arm64', smoke-cmd: 'bash scripts/mcp-smoke.sh', smoke-target: 'build/bin/nodeshell' }`
  - `{ platform: linux, runner: ubuntu-latest, wails-args: '-platform linux/amd64', smoke-cmd: 'bash scripts/mcp-smoke.sh', smoke-target: 'build/bin/nodeshell' }`
- Linux dep install wrapped in `if: matrix.platform == 'linux'`; NSIS install and macOS DMG steps are deferred to Task 2.

Repository dispatch: the implementer must fill in concrete YAML with these steps, using `matrix.*` substitution, and a `name:` field per job. The smoke script invocation must use a shell step that expands `$GITHUB_WORKSPACE`.

- [ ] **Step 6: Run the guard test to verify GREEN**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: PASS — ci.yml exists, stale workflows absent, three platforms targeted, tests before packaging, artifacts uploaded.

- [ ] **Step 7: Verify local YAML is well-formed**

Run: `node -e "require('js-yaml').load(require('fs').readFileSync('.github/workflows/ci.yml','utf8')); console.log('YAML valid')"` (if `js-yaml` unavailable, skip; CI parsing is the real gate)
Then run full local verification: `node node_modules/vitest/vitest.mjs run` (expect 95+ tests), `npx tsc --noEmit -p tsconfig.test.json`, `git diff --check`.

---

### Task 2: Platform packaging — Windows NSIS, macOS DMG/ZIP, Linux AppImage/Deb

**Covers:** S2.4, S5.2

**Files:**
- Modify: `.github/workflows/ci.yml` (add packaging steps per matrix entry)
- Create: `scripts/package-linux.sh` (AppImage via appimagetool; Deb/Pacman via nfpm)
- Create: `scripts/package-macos.sh` (DMG via hdiutil; ZIP via ditto/codesign ad-hoc)
- Modify: `tests/ci-workflow-structure.test.ts` (add asserts for packaging outputs)

**Interfaces:**
- Consumes: `build/bin/nodeshell*` binary from Task 1.
- Produces: platform installer/archive artifacts (`*.exe` NSIS installer, `*.dmg`+`*.zip` macOS, `*.AppImage`+`*.deb` linux) uploaded as the artifact for Task 3's release job.

- [ ] **Step 1: Write the RED guard assertions for packaging**

Append to `tests/ci-workflow-structure.test.ts`:

```ts
import { existsSync as _exists } from 'node:fs'

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
```

- [ ] **Step 2: Run to verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: FAIL — packaging steps and signing tokens absent.

- [ ] **Step 3: Add Windows NSIS packaging to ci.yml**

In the `windows` matrix step, after `wails build`:
- `choco install nsis -y --no-progress` (makensis not preinstalled on windows-latest)
- `wails build -platform windows/amd64 -nsis -skipbindings` (regenerates with NSIS installer into `build/bin/` as `nodeshell-amd64-installer.exe`)
- Add a conditional signing step guarded by `${{ secrets.WINDOWS_SIGN != '' }}` that runs `signtool sign /f cert.pfx /p $WINDOWS_SIGN ...` — when the secret is absent, this step is skipped (allowed by spec S2.4). Leave a clearly marked comment: `# WINDOWS_SIGN injection point — skipped when unconfigured`.

- [ ] **Step 4: Add macOS DMG/ZIP packaging via scripts/package-macos.sh**

Create `scripts/package-macos.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
bin="$1"          # build/bin/nodeshell.app
out="$2"          # build/bin
name="$3"         # NodeShell-2.0.0-macos-arm64
# Ad-hoc sign the app bundle (no signing material configured → test product)
codesign --force --deep --sign - "$bin"
# ZIP
ditto -c -k --keepParent "$bin" "$out/$name.zip"
# DMG
hdiutil create -volname "NodeShell" -srcfolder "$bin" -ov -format UDZO "$out/$name.dmg"
# APPLE_SIGN injection point — signtool/notarization would go here if configured
echo "Packaged $name: $out/$name.zip $out/$name.dmg"
```

In `ci.yml` macos step, after `wails build -platform darwin/arm64 -skipbindings`:
- `bash scripts/package-macos.sh build/bin/nodeshell.app build/bin NodeShell-2.0.0-macos-arm64`
- Comment: `# APPLE_SIGN injection point — notarize when configured`.

- [ ] **Step 5: Add Linux AppImage/deb via scripts/package-linux.sh**

Create `scripts/package-linux.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
bin="$1"          # build/bin/nodeshell
out="$2"          # build/bin
name="$3"         # NodeShell-2.0.0-linux-amd64
arch="${4:-amd64}"
appdir="$out/$name.AppDir"
mkdir -p "$appdir/usr/bin"
cp "$bin" "$appdir/usr/bin/nodeshell"
mkdir -p "$appdir/usr/share/applications"
cat > "$appdir/usr/share/applications/nodeshell.desktop" <<EOF
[Desktop Entry]
Name=NodeShell
Exec=nodeshell
Type=Application
Icon=nodeshell
Categories=Utility;
EOF
cp build/appicon.png "$appdir/nodeshell.png"
mkdir -p "$appdir"
cat > "$appdir/AppRun" <<EOF
#!/usr/bin/env bash
exec "\$(dirname "\$0")/../usr/bin/nodeshell" "\$@"
EOF
chmod +x "$appdir/AppRun"
# AppImage requires appimagetool on PATH
appimagetool "$appdir" "$out/$name.AppImage"
# deb/pacman via nfpm
cat > "$out/nfpm.yaml" <<EOF
name: nodeshell
arch: $arch
platform: linux
version: 2.0.0
maintainer: AzureLandin
description: NodeShell SSH client
homepage: https://github.com/AzureLandin/Simple-SSH-Client
license: MIT
contents:
  - src: $bin
    dst: /usr/bin/nodeshell
  - src: $out/nodeshell.desktop
    dst: /usr/share/applications/nodeshell.desktop
  - src: build/appicon.png
    dst: /usr/share/icons/nodeshell.png
EOF
nfpm pkg --config "$out/nfpm.yaml" --packager deb --target "$out/$name.deb"
nfpm pkg --config "$out/nfpm.yaml" --packager pacman --target "$out/$name.pkg.tar.zst"
echo "Packaged $name: $out/$name.AppImage $out/$name.deb $out/$name.pkg.tar.zst"
```

In `ci.yml` linux step, install `appimagetool` and `nfpm`:
- `sudo apt-get install -y appimagetool` (or download from releases if not in apt)
- `go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`
- After `wails build`: `bash scripts/package-linux.sh build/bin/nodeshell build/bin NodeShell-2.0.0-linux-amd64 amd64`

- [ ] **Step 6: Update artifact upload paths in ci.yml**

Windows: upload `build/bin/*installer.exe`
macOS: upload `build/bin/*.dmg`, `build/bin/*.zip`, `build/bin/nodeshell.app`
Linux: upload `build/bin/*.AppImage`, `build/bin/*.deb`, `build/bin/*.pkg.tar.zst`, `build/bin/nodeshell`

- [ ] **Step 7: Run guard test to verify GREEN**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: PASS — NSIS, hdiutil/ditto, appimagetool/nfpm, signing tokens all present in ci.yml.

- [ ] **Step 8: Full local verification**

Run: `node node_modules/vitest/vitest.mjs run` (full Vitest), `npx tsc --noEmit -p tsconfig.test.json`, `git diff --check`. (Cannot run `wails build` packaging locally on Windows for mac/linux; CI is the real gate.)

---

### Task 3: Tag-triggered GitHub Release with signing injection points

**Covers:** S2.4, S5.2

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `tests/ci-workflow-structure.test.ts` (add release.yml assertions)

**Interfaces:**
- Consumes: `NodeShell-2.0.0-*` artifacts uploaded by `ci.yml` build jobs.
- Produces: A GitHub Release on `v*` tags with all three-platform installers/archives attached, gated by `permissions: contents: write`.

- [ ] **Step 1: Write the RED guard for release.yml**

Append to `tests/ci-workflow-structure.test.ts`:

```ts
describe('release workflow', () => {
  it('has .github/workflows/release.yml', () => {
    expect(() => readYaml('.github/workflows/release.yml')).not.toThrow()
  })

  it('triggers on v* tags', () => {
    const yaml = readYaml('.github/workflows/release.yml')
    expect(yaml).toMatch(/tags:\s*\n\s*-\s+['"]?v\*/)
  })

  it('needs write permission to contents', () => {
    const yaml = readYaml('.github/workflows/release.yml')
    expect(yaml).toMatch(/permissions:[\s\S]*contents:\s*write/)
  })

  it('downloads artifacts and creates a release', () => {
    const yaml = readYaml('.github/workflows/release.yml')
    expect(yaml).toContain('download-artifact')
    expect(yaml).toMatch(/action-gh-release|gh release create/)
  })
})
```

- [ ] **Step 2: Run to verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: FAIL — release.yml absent.

- [ ] **Step 3: Create release.yml**

Create `.github/workflows/release.yml`:

- Triggers: `push: tags: ['v*']` + `workflow_dispatch`.
- `permissions: contents: write`.
- A `release` job on `ubuntu-latest`:
  1. `actions/checkout@v4`
  2. `actions/download-artifact@v4` with `path: artifacts` (downloads all `NodeShell-2.0.0-*` artifacts from the triggering ci run). Note: because upload/download-artifact v4 needs a run id, use the `${{ github.run_id }}` of the ci run that triggered; when triggered by tag, ci.yml runs first on the tag (since it also matches `v*`) and uploads artifacts, then release.yml runs and downloads by `run_id: ${{ github.run_id }}`. If artifact availability is unreliable, an alternative is to put the release steps inside ci.yml as a job that `needs: build` and downloads `${{ github.run_id }}` — the implementer should choose the pattern that reliably chains. Recommended: keep release.yml separate but use `actions/download-artifact@v4` with `github-token` and `run-id: ${{ github.run_id }}`.
  3. `softprops/action-gh-release@v2` with `files: artifacts/**/*`, `draft: true`, `generate_release_notes: true`.
  4. Signing injection point comment: `# When secrets.WINDOWS_SIGN/APPLE_SIGN are configured, a preceding job could sign the artifacts before release. Injection points are in ci.yml packaging steps.`

- [ ] **Step 4: Run guard test to verify GREEN**

Run: `node node_modules/vitest/vitest.mjs run tests/ci-workflow-structure.test.ts`
Expected: PASS — release.yml exists, triggers on `v*`, write permission, downloads artifacts, creates release.

- [ ] **Step 5: Full verification and spec T9 checkbox**

Run full local suite: `node node_modules/vitest/vitest.mjs run` (expect 98+ tests), `npx tsc --noEmit -p tsconfig.test.json`, `node node_modules/eslint/bin/eslint.js . --no-cache --quiet`, `git diff --check`. Then check the spec checkbox: edit `docs/compose/spec/wails-rebuild.md` line 133 from `- [ ] T9:` to `- [x] T9:`.

- [ ] **Step 6: Final diff review**

Review `git diff --check` and `git status --short` — no staged changes, all additions are new workflow/yaml/shell scripts + one guard test + spec checkbox. No production Go or renderer source modified by this task.