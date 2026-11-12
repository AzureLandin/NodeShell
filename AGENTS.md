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
- Use the project Go 1.26 toolchain for Wails. On this macOS host, Go 1.27 package analysis failed with `package "errors" without types`; `GOTOOLCHAIN=go1.26.0 wails build -skipbindings` worked without dependency changes. The generated default `build/darwin/Info.plist` is ignored; explicitly track a custom template if packaging needs one.
- Ubuntu 24.04/CI has only webkit2gtk 4.1: add `-tags webkit2_41` (e.g. `-platform linux/amd64 -tags webkit2_41`). Windows release/MCP builds use `-windowsconsole` so `--mcp` stdio works under redirected handles; do not drop it.

## Verify

- Full check: `npm run typecheck`, `npm test`, `npm run lint`, `npm run build:wails`, `go vet ./...`, then Go tests.
- Single frontend test: `npm test -- tests/ui/settings.test.tsx`.
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
- Frontend business code lives in `features/<domain>/`; cross-feature and application imports use each feature's explicit `index.ts`. Features must not import `app` / `App`, and `ui` must not import features or app composition. Do not recreate the old `components/` / `hooks/` compatibility entries. See `docs/design/frontend-structure.md`.
- Keep `App.tsx` focused on providers, layout and wiring. `app/AppOverlays.tsx` composes dialogs; `features/sessions/useConnectionFlow.ts` coordinates password, host-key and credential decisions through a reducer, while `useSessions` owns session state and subscriptions. Preserve synchronous duplicate-command guards and lifetime checks after async work.
- Application confirmations use `app/use-confirmation-queue.ts`: FIFO, identity-bound completion, all waiting promises resolve false on disposal. Backend permission requests belong to `features/permissions/usePermissionQueue.ts`: deduplicate, guard pending decisions, allow failed decisions to retry, and unsubscribe on disposal; backend session/timeout policy owns their termination.
- `features/settings/useAppSettings.ts` owns non-sensitive preferences, optimistic UI and confirmed rollback baselines. Serialize writes per field, ignore obsolete results, protect edited values from late bootstrap reads, and retain font-size debounce/disposal cleanup. Settings remain outside the host query pilot.
- Feature styles live in the feature's CSS Module. `theme.css` owns variables, `ui/surfaces.css` owns shared surfaces, and `App.css` owns application layout. Use explicit `:global(...)` for shared/vendor classes inside modules; pass Portal classes explicitly. Shared `settings-modal-header` / `settings-modal-close` also serve host/SFTP dialogs, so keep their base styles in `ui`.
- Import Base UI only from `src/renderer/src/ui/`; business components use the local wrappers. Use `Button` for shared action styles and loading behavior, and `IconButton` with an explicit `aria-label` for icon actions. Keep vendor types derived from component Props rather than copying their event unions.
- Use `TabsRoot` / `TabsList` / `TabsTab` / `TabsPanel` from `ui/tabs.tsx`. Sidebar and session panels use `keepMounted` to preserve subscriptions and terminal instances; session IDs remain stable keys. Keep close buttons beside tab triggers and toolbar actions outside the tablist. Session selection after closing belongs to `useSessions`.
- Context-menu hosts own one nullable target/position; keep the controlled menu mounted and let `ui/context-menu.tsx` handle dismissal and focus. It uses public non-modal Menu parts with a virtual anchor for external opening. Do not add global close listeners or use test IDs for production behavior. Terminal keyboard gating belongs to xterm's custom key handler; ordinary Escape must reach SSH after the menu closes.
- `useModalClose()` requests the exit transition; `ModalShell.onClose` notifies the owner after Base UI completes it. Preserve busy/password outside-dismissal policies and nested Escape propagation rather than unmounting on the initial close request.
- Install `NotificationProvider` once at the application root and publish messages with `useNotify()` from `ui/use-notify.ts`. Toast primitives and the manager stay inside `ui/`; do not bridge React message state into the manager. Hooks report errors through injected callbacks, while the app chooses localized text and notification policy.
- Notifications replace the current message rather than stack. The default timeout is `0` (manual dismissal); repeating a message refreshes an explicit timeout and may reopen a dismissed notification. Do not add a second timer or message store outside the provider.
- `HostForm` is the scoped React Hook Form pilot; keep its pure defaults/validation/DTO conversion in `features/hosts/host-form-model.ts`. Form-library types stay in the form, while `FormField` owns label/help/error associations. Do not spread the pilot to simple dialogs by default or add Zod.
- Host drafts belong to an editing identity: ordinary renders preserve them, changing target resets them. Password defaults are always empty; clear passwords on auth change, successful save and disposal, keep failed drafts available for retry, and never include passwords in HostInput or global caches. Preserve blank edit credential semantics. Saving blocks modal dismissal and target changes; stale picker/save results must not affect another editor.
- Install one `AppQueryProvider` at the app root. The TanStack Query pilot owns only host-list reads through `features/hosts/hosts-query.ts` and `useHosts`; use an in-memory client with retry/focus/reconnect refetch disabled and `networkMode: 'always'` for local IPC. Opening host management and credential decisions explicitly refresh authoritative data. Query cancellation ignores late frontend results; it does not cancel the Wails backend call.
- Host create/update/remove and credential writes remain explicit commands, outside the mutation cache. Reconcile confirmed host writes before refreshing; a later read failure must not reject a successful write. Host read errors belong to the manager's local retry state, not a toast. Never cache passwords or API keys; settings/SFTP/tunnel state is outside this pilot.
- SFTP listings and commands belong to a session/connection lifetime. Clear cached rows and modal targets on identity changes, ignore late results after disposal, and serialize panel directory work because backend `chdir` mutates cwd. Use absolute entry paths for file actions and reconcile authoritative cwd/list after failed queued navigation. Collapse alone preserves the current session cache.
- Tunnel reads and commands have separate revisions and a session lifetime. Invalidate earlier reads when sending a command, block duplicate commands synchronously, stop unsent batch items after a session change, and never let old completion/error/finally callbacks update a new session. Do not automatically reverse an already-sent backend command when its UI owner disappears.
- Vite root is `src/renderer` and output is `frontend/dist`. Vitest only includes `tests/**/*.test.{ts,tsx}`.
- Go errors crossing IPC implement `ErrorCode() string` using codes from `internal/apperror`; the frontend whitelist is `SSH_CODES` in `src/shared/ipc-error.ts`. Keep both lists in sync, or codes collapse to `UNKNOWN`.
- MCP exposes exactly 10 tools. The count is asserted in `internal/mcpcli/runmcp_test.go` and both `scripts/mcp-smoke.*`; update all of them (and the README) when changing tools. `--mcp` must write protocol only to stdout and nothing to stderr during the smoke test.
- The product is Wails-only: `tests/contracts/runtime-dependencies.test.ts` rejects Electron/`ssh2`/MCP SDK dependencies and generic `dev`/`build` scripts. Do not reintroduce them.

## Security and data invariants

- SSH host keys are verified before authentication; do not bypass `internal/knownhosts`.
- Passwords and agent API keys live in the OS keyring, not JSON. Never write secrets into `hosts.json` or `settings.json`.
- Local upload/download, private-key, and MCP file paths must pass `internal/localpathguard` and stay under the user home. Remote SFTP paths use POSIX normalization. Do not add permissive path handling.
- Data lives in `os.UserConfigDir()/nodeshell`, intentionally compatible with the Electron 2.0.0 layout. The old `credentials.json` must remain untouched.

## Testing conventions

- Frontend tests live in `tests/unit/` (logic and adapters), `tests/ui/` (components and interactions), and `tests/contracts/` (CI and dependency constraints). Shared setup, fake APIs, and payloads live in `tests/support/`. See `tests/README.md` for commands and placement rules.
- Vite, Vitest, and detailed TypeScript configs live in `config/`; npm scripts select them explicitly. Root `tsconfig.json` remains the editor project-reference entry. Keep Go tests beside their packages, including entry-package tests at the repository root.
- Vitest defaults to the `node` environment; DOM tests need the `// @vitest-environment jsdom` docblock.
- Vitest uses non-scoped CSS Module names for stable behavior-test selectors; production Vite still scopes them. These tests do not verify CSS cascade or native WebView layout; keep visual acceptance separate.
- Use `tests/support/helpers.tsx` (`installFakeApi`, `renderWithI18n`, event emitters) instead of hardcoding translations or mocking component behavior. `renderWithI18n` retains its provider across rerenders and accepts an optional i18n instance; rerender the component directly instead of adding another provider. Use `tests/support/deferred.ts` for controlled late IPC responses.
- Go integration tests use the real in-process SSH server in `internal/sshtest`; tests must not depend on the public network or an external SSH daemon. Root-package tests use `testApp` from `app_test.go` with a fake keyring.
- UI tests subscribe through the fake `window.api`; event names must match the Go constants.

## CI and release

- `.github/workflows/ci.yml` is the source of truth for packaging flags and platform dependencies; the README is a summary.
- Windows, macOS, and Linux jobs build with Wails and run `scripts/mcp-smoke.*`. Packaging must run on native runners; do not assume local cross-platform Wails builds work.
- `tests/contracts/ci-workflow-structure.test.ts` asserts CI structure and version handling; update it when intentionally changing the workflow.
- `package.json` is the version source for CI (`APP_VERSION`). Keep `wails.json` `productVersion` and the README version in sync. Tags matching `v*` produce a draft GitHub release.
- Secrets are mapped to job-level env vars; never reference `secrets.*` in a step-level `if:` (GitHub rejects the workflow).

## Docs

`README.md` is the user-facing document (Chinese). `docs/design/wails-rebuild.md` and `docs/reports/` are historical design/acceptance notes, grouped by domain; they are not current authority, so verify claims against code and CI. Start at `docs/README.md` for the index. Put new design notes under `docs/design/` and reports under `docs/reports/<domain>/`, not at the repository root.
