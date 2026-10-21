# AGENTS.md

## Project

NodeShell is a Wails v2 desktop SSH client: Go backend, React/TypeScript frontend.
`main.go` has two entries: the GUI (default) and a native MCP stdio server (`--mcp`). `internal/app/app.go` is the Wails binding facade; domain logic lives in the other `internal/*` packages.

Toolchain: Go 1.26, Node 22 (CI), Wails v2.13. The Wails CLI is not an npm dependency.

## Setup and build order

- `npm ci && go mod download`
- `npm run build:wails` emits `frontend/dist/`, which `main.go` embeds with `//go:embed all:frontend/dist`. On a clean checkout, `go test .` and `go vet ./...` fail until this exists; `wails build` creates it automatically. `internal/*` packages can be tested without it.
- `npm run dev:wails` only starts Vite on port 5173; use `wails dev` for the desktop app.
- Build with `wails build -skipbindings`. Install the CLI with `go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`.
- Ubuntu 24.04/CI has only webkit2gtk 4.1: add `-tags webkit2_41` (e.g. `-platform linux/amd64 -tags webkit2_41`). Windows release/MCP builds use `-windowsconsole` so `--mcp` stdio works under redirected handles; do not drop it.

## Verify

- Full check: `npm run typecheck`, `npm test`, `npm run lint`, `npm run build:wails`, `go vet ./...`, then Go tests.
- Single frontend test: `npx vitest run tests/ui/settings.test.tsx`.
- Single Go test: `go test ./internal/mcpcli -run TestName -count=1`. Root-package tests use `go test . -run TestName -count=1` and require `frontend/dist`.
- With `node_modules` present, filter it from Go package lists like CI: `go test $(go list ./... | grep -v /node_modules/)`. CI runs one package at a time with `-timeout 90s`; keep test waits bounded.
- CI keeps `-race` off on every platform (matrix entries set `go-test-race: ''`). Local race runs have a history of hanging on Linux/macOS.

## Architecture and API contract

- Root-level `main.go`, `console_*.go`, and `windowsize.go` are the desktop entry package: `//go:embed frontend/dist` is root-relative and the Wails `Bind` call lives there. The App facade and its tests are in `internal/app`; keep Go tests beside their package.
- Keep domain packages (`internal/*` except `internal/app`) free of Wails imports. Services receive dependencies and an `EventSink`; `internal/app/wails_sink.go` adapts events to the runtime. This is what keeps them headless-testable.
- Add a bound API in three places: an exported `<Group><Method>` method on `*App` in `internal/app/app.go`, a typed call in `src/renderer/src/api/adapter.ts`, and shared DTOs/event constants in `src/shared/types.ts`. The frontend calls `window.go.app.App` directly.
- App lifecycle is driven by package functions `app.Start`/`app.Stop`; do not turn them into methods, or Wails binds them as frontend-callable APIs.
- `wailsjs/` is generated and gitignored; do not edit or commit it. `frontend/dist/` is build output.
- The frontend intentionally keeps the old `window.api` / `ElectronApi` names even though the runtime is Wails; do not rename the contract.
- Vite root is `src/renderer` and output is `frontend/dist`. Vitest only includes `tests/**/*.test.{ts,tsx}`.
- Go errors crossing IPC implement `ErrorCode() string` using codes from `internal/apperror`; the frontend whitelist is `SSH_CODES` in `src/shared/ipc-error.ts`. Keep both lists in sync, or codes collapse to `UNKNOWN`.
- MCP exposes exactly 10 tools. The count is asserted in `internal/mcpcli/runmcp_test.go` and both `scripts/mcp-smoke.*`; update all of them (and the README) when changing tools. `--mcp` must write protocol only to stdout and nothing to stderr during the smoke test.
- The product is Wails-only: `tests/runtime-dependencies.test.ts` rejects Electron/`ssh2`/MCP SDK dependencies and generic `dev`/`build` scripts. Do not reintroduce them.

## Security and data invariants

- SSH host keys are verified before authentication; do not bypass `internal/knownhosts`.
- Passwords and agent API keys live in the OS keyring, not JSON. Never write secrets into `hosts.json` or `settings.json`.
- Local upload/download, private-key, and MCP file paths must pass `internal/localpathguard` and stay under the user home. Remote SFTP paths use POSIX normalization. Do not add permissive path handling.
- Data lives in `os.UserConfigDir()/nodeshell`, intentionally compatible with the Electron 2.0.0 layout. The old `credentials.json` must remain untouched.

## Testing conventions

- Vitest defaults to the `node` environment; DOM tests need the `// @vitest-environment jsdom` docblock.
- Use `tests/ui/helpers.tsx` (`installFakeApi`, `renderWithI18n`, event emitters) instead of hardcoding translations or mocking component behavior.
- Go integration tests use the real in-process SSH server in `internal/sshtest`; tests must not depend on the public network or an external SSH daemon. Root-package tests use `testApp` from `app_test.go` with a fake keyring.
- UI tests subscribe through the fake `window.api`; event names must match the Go constants.

## CI and release

- `.github/workflows/ci.yml` is the source of truth for packaging flags and platform dependencies; the README is a summary.
- Windows, macOS, and Linux jobs build with Wails and run `scripts/mcp-smoke.*`. Packaging must run on native runners; do not assume local cross-platform Wails builds work.
- `tests/ci-workflow-structure.test.ts` asserts CI structure and version handling; update it when intentionally changing the workflow.
- `package.json` is the version source for CI (`APP_VERSION`). Keep `wails.json` `productVersion` and the README version in sync. Tags matching `v*` produce a draft GitHub release.
- Secrets are mapped to job-level env vars; never reference `secrets.*` in a step-level `if:` (GitHub rejects the workflow).

## Docs

`README.md` is the user-facing document (Chinese). `docs/design/wails-rebuild.md` and `docs/reports/` are historical design/acceptance notes, grouped by domain; they are not current authority, so verify claims against code and CI. Start at `docs/README.md` for the index. Put new design notes under `docs/design/` and reports under `docs/reports/<domain>/`, not at the repository root.
