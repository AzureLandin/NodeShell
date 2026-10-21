---
feature: wails-rebuild
status: in-progress
updated: 2026-08-01
branch: refactor/wails-rebuild
commits:
---

# Go + Wails 重构

## Report

## [S1] 问题与目标

NodeShell 当前以 Electron 39、Node.js 和 `ssh2` 实现。重构目标是在 Wails v2 中以 Go 实现全部桌面后端和 MCP 服务，同时最大限度保留现有 React/Vite/xterm 前端、视觉样式、交互和用户数据，使 Windows x64、macOS ARM64、Linux x64 三个平台的首个稳定版达到现有功能等价。

稳定性优先于重写幅度。迁移采用前端兼容层和后端按领域拆分的方式；不增加现有产品没有的功能，不把 Node.js 作为成品运行时或 sidecar 保留下来。

## [S2] 产品与兼容边界

### [S2.1] 保留的用户功能

- 主机配置的新增、编辑、删除、列表与快速连接。
- 密码和私钥认证、keyboard-interactive 密码应答、连接取消、首次主机密钥确认、主机密钥变更警告、断线状态和安全重连。
- 多会话标签、交互式 `xterm-256color` 终端、终端缩放、字体设置、明暗/跟随系统主题和中英文界面。
- SFTP 目录浏览、切换、新建、重命名、递归删除、文件选择上传、拖放上传、下载和传输进度。
- 远端 Linux CPU、内存、Swap、负载、网络与进程监控；非 Linux 或命令不可用时显示现有错误/空状态，不影响 SSH 会话。
- MCP 设置、状态检查、复制配置、向 Cursor、Claude Code、Codex、OpenCode 注册，以及现有十个 MCP 工具。
- 关于页、应用版本、系统字体枚举、系统文件对话框、外部链接和 Toast/确认框体验。

### [S2.2] 前端兼容策略

保留 `src/renderer` 下的 React 组件、CSS、图片、i18next 语言包、xterm.js 和现有状态模型。构建入口改为普通 Vite/Wails 前端。新增薄 TypeScript 适配层，继续向组件暴露当前 `window.api` 形状，内部调用 Wails 生成的 Go bindings、runtime events、文件对话框和文件拖放接口。

组件布局、文案、快捷操作、主题 token 和响应式行为原则上不改。只有 Electron 专属行为、Wails WebView 平台差异或已证明的稳定性缺陷允许做最小调整，并须有针对性验证。

### [S2.3] 用户数据

应用继续使用操作系统 application-data 下名为 `nodeshell` 的目录，并兼容读取现有：

- `hosts.json` 的 `{ "hosts": HostConfig[] }`；
- `settings.json` 的语言、主题、终端字体和 MCP 策略字段；
- `known_hosts.json` 的 `host:port -> SHA256 base64` 映射。

所有文件写入保持“同目录临时文件 + 原子替换”，写入失败不得破坏最后一个有效版本。未知 JSON 字段在无需修改该记录时不得导致启动失败。

旧 `credentials.json` 的 Electron `safeStorage` 密文不迁移、不解密，文件保持原状以便回退旧版本。Wails 首次加载主机时将旧的 `credentialsSaved` 视为无可用凭据；用户下次连接重新输入并选择保存后，密码或私钥内容写入操作系统 Keyring，主机的保存状态再更新。删除主机或清除凭据时同步删除 Keyring 项。

### [S2.4] 平台与发布

首个稳定版同时支持 Windows x64、macOS ARM64、Linux x64。最低产物为 Windows 安装包、macOS `.app` 加 DMG/ZIP、Linux AppImage/Deb/Pacman；Snap 可在 Wails 打包链可重复后保留，否则不得阻塞其余 Linux 产物，但须在发布说明中明确。

构建必须在对应原生 CI runner 上进行。未配置签名材料时允许生成未签名测试产物，但发布流程须保留 Windows 签名与 macOS 签名/公证的注入点。

## [S3] Go 后端设计

### [S3.1] 进程与服务结构

程序入口先解析参数：默认启动 Wails GUI；`--mcp` 直接运行原生 Go MCP stdio 服务且不初始化 WebView。GUI 绑定一个窄 App facade，具体职责分为 hosts、settings、credentials、known-hosts、sessions、SFTP、monitor、fonts、MCP registration 和 dialogs。

服务通过构造函数接收数据目录、事件发送器和系统能力，避免依赖全局 Wails runtime。会话映射和可变共享状态使用 mutex 保护；每次连接、传输、exec 和监控轮询都有独立 context/cancel，不使用全局单一“正在连接”句柄。

### [S3.2] SSH 会话契约

使用 `golang.org/x/crypto/ssh`：连接超时 10 秒并保留 10.5 秒应用硬截止；默认关闭压缩；认证前执行 known-hosts 校验；密码认证同时支持 keyboard-interactive；私钥内容只从 Keyring 或 Home 目录内已验证路径读取。

连接成功后申请 `xterm-256color` PTY，初始尺寸 80x24；输入写入 stdin，resize 调用 SSH window-change。每个 session 具有唯一 ID，关闭事件最多发送一次。重连必须先建立新会话，成功后再替换旧会话，失败时旧会话保持可用。

终端输出以每 12ms 或累计 48KiB 为阈值合并发送 Wails event。前端未挂载会话继续使用 96KiB 有界 ring buffer，xterm scrollback 保持 1000 行；验证事件顺序、UTF-8 分片和持续输出下的内存上限。

错误保持稳定 code：`AUTH_FAILED`、`HOST_KEY_UNKNOWN`、`HOST_KEY_CHANGED`、`TIMEOUT`、`CANCELLED`、`CONNECTION_REFUSED`、`HOST_NOT_FOUND`、`SESSION_NOT_FOUND`、`UNKNOWN`。Go binding 错误由适配层规范化为现有 `ConnectError`/Toast 行为，不把内部堆栈暴露给 UI。

### [S3.3] SFTP 与路径安全

使用 `github.com/pkg/sftp`，每个 SSH session 延迟创建并复用一个 SFTP client，session 关闭时释放。远端路径继续按 POSIX 规则归一化；递归删除不得越过用户指定目标。上传、下载和 MCP 本地文件参数在解析符号链接后必须位于用户 Home 内；部分文件和临时文件清理不应覆盖已存在的完整目标。

GUI 文件选择、保存和拖放由 Wails runtime 提供路径给 Go，Go 再执行相同 Home 边界校验。传输进度事件保留 sessionId、方向、名称、已传输、总量和完成标志。

### [S3.4] 监控

保留每 4 秒一次的远端 Linux `/proc` 采样和现有解析语义。只有当前活动 session 轮询；切换或关闭时取消旧 context。单次命令保留超时和 2MiB stdout 上限，错误作为 monitor update 事件返回，不关闭终端。

### [S3.5] MCP

同一可执行文件的 `--mcp` 模式使用 stdin/stdout 实现 MCP transport，不启动 GUI、不输出非协议文本到 stdout。提供现有工具：`list_hosts`、`list_sessions`、`connect_host`、`disconnect_session`、`run_command`、`sftp_list`、`sftp_read`、`sftp_write`、`sftp_upload`、`sftp_download`。

MCP 会话与 GUI 会话彼此独立；默认最多 8 个、空闲 10 分钟回收，正在执行命令的会话不回收。远程命令与文本读写保留超时、2MiB exec 输出上限、512KiB MCP 文件上限及 Home 路径限制。

四类 MCP 注册器改为写入 NodeShell 可执行文件绝对路径及 `--mcp` 参数，并保持原配置中其他 server/字段不变。旧 `node .../nodeshell-mcp.mjs` 注册应识别为 stale 并可原地升级。成品不包含 Node relay，不要求用户安装 Node.js。

## [S4] Wails API 与事件契约

TypeScript 适配层维持当前 `window.api` 分组和 Promise 返回形状：`hosts`、`sessions`、`settings`、`credentials`、`sftp`、`files`、`monitor`、`fonts`、`app`、`mcpRegistration`、`dialog`。

事件名称或适配后的回调语义覆盖：session data/closed/error、SFTP transfer progress、monitor update。每个 `onX` 返回可重复安全调用的 unsubscribe。终端输入允许 fire-and-forget，但 Go 侧必须序列化同一 session 的写入；其他操作返回可观察成功或结构化失败。

Go 绑定生成物由 Wails 构建生成，不手工维护重复接口。共享 JSON DTO 使用明确字段名，与现有 TypeScript 类型兼容；时间继续使用 Unix 毫秒。

## [S5] 稳定性与验证

### [S5.1] 自动化测试

- Go 单元测试覆盖原子存储、旧 JSON 兼容、Keyring 抽象、known-hosts、错误映射、路径守卫、monitor 解析、SFTP 路径、MCP 配置合并、会话限制/回收和输出合并上限。
- 使用本地临时 SSH/SFTP 测试服务或可控 fake transport 覆盖密码、keyboard-interactive、私钥、未知/变更 host key、连接取消、PTY 输入输出、resize、exec 超时和连接关闭；测试不得依赖公网。
- 前端保留现有 TypeScript/Vitest 测试，并新增适配层契约测试和关键 React 交互测试：主机 CRUD、host-key 确认、连接/重连、标签切换、SFTP 操作、设置和 MCP 注册状态。
- 原生 Go MCP 模式进行 stdio 协议测试，验证 initialize、工具列表、每个工具 schema、错误响应和 stdout 无污染。
- 终端吞吐测试验证持续输出时事件合并、顺序、UTF-8 完整性与有界内存；拖放及系统 Keyring 在三平台做 smoke test。

### [S5.2] CI 与验收

每个变更先运行 Go tests、前端 tests、TypeScript typecheck、lint 和 Wails build。三平台 CI 均执行测试后再打包；产物安装/启动 smoke test 至少验证应用启动、数据加载、版本 API 和 `NodeShell --mcp` 握手。

发布候选需用可控 SSH 测试端逐项通过功能清单，并验证从 Electron 2.0.0 数据目录启动：主机、设置、known-hosts 可读；旧凭据不被误用；重新保存后 Keyring 可用；回退 Electron 时旧 credentials 文件仍存在。

## [S6] 范围外

- 不迁移或解密 Electron `safeStorage` 旧凭据。
- 不新增本地 shell、跳板机、端口转发、SSH agent、同步目录或自动更新功能。
- 不重设计现有界面、品牌和信息架构。
- 不以 Node sidecar 或嵌入 Electron 作为长期兼容方案。
- 不承诺当前代码未实现的 Windows/macOS 远端系统监控解析；监控目标仍是远端 Linux。

## Tasks

- [x] T1: 建立 Wails v2/Go/Vite 工程与 `window.api` 适配骨架 — acceptance: 三平台构建入口存在，现有 React 主界面可在 Wails dev/build 中渲染，适配层契约测试通过（covers: S2.2, S3.1, S4）
- [x] T2: 迁移主机、设置、known-hosts 和原子持久化 — acceptance: Go 测试读取现有 fixture 并无损完成 CRUD，坏写入不破坏上一个版本（covers: S2.3, S3.1; depends: T1）
- [x] T3: 实现新 Keyring 凭据存储与旧凭据失效语义 — acceptance: 旧密文不被读取、旧文件不被修改，重新保存/读取/清除和删除主机联动测试通过（covers: S2.3, S3.1; depends: T2）
- [x] T4: 实现 Go SSH 会话与 Wails 终端事件 — acceptance: 可控 SSH 测试覆盖两种认证、host key、取消、PTY、resize、重连、关闭及输出边界，现有终端 UI 可交互（covers: S2.1, S3.2, S4, S5.1; depends: T2, T3）
- [x] T5: 实现 Go SFTP、系统对话框与拖放 — acceptance: 列表/切换/新建/重命名/递归删除/上传/下载/进度均通过集成测试，Home 逃逸被拒绝（covers: S2.1, S3.3, S4; depends: T4）
- [x] T6: 迁移远端监控与字体/版本/外链系统能力 — acceptance: 监控解析和取消测试通过，现有侧栏、字体和关于页在 Wails 中工作（covers: S2.1, S3.4, S4; depends: T4）
- [x] T7: 实现原生 Go MCP 模式与四类注册 — acceptance: `NodeShell --mcp` 完成协议测试，十个工具和策略边界通过测试，四类配置可新增/升级且保留无关内容（covers: S2.1, S3.5; depends: T2, T3, T4, T5）
- [x] T8: 清除 Electron/Node 运行时后端并完成前端等价测试 — acceptance: 成品不含 Electron、ssh2、Node MCP relay，关键 UI 交互测试通过且视觉结构无非必要变化（covers: S1, S2.1, S2.2, S6; depends: T5, T6, T7）
- [x] T9: 建立三平台 CI、打包和安装 smoke tests — acceptance: Windows x64、macOS ARM64、Linux x64 均测试并生成约定产物，应用与 MCP smoke test 通过（covers: S2.4, S5.2; depends: T8）
- [x] T10: 执行迁移、协议、性能和三平台发布候选验收 — acceptance: S5 全部验证有新鲜通过证据且无未解决 critical review finding（covers: S5.1, S5.2; depends: T9）
