# MCP 权限外置化：实施与验收报告

- **实施日期**：2026-09-02
- **关联计划**：[MCP权限外置化_编程Agent实施计划.md](MCP权限外置化_编程Agent实施计划.md)
- **状态**：**自动化门禁通过 (PASS)**

---

## 1. 权限调用链（新架构）

```text
GUI 内置 Agent
  AgentPanel → mcpcli.Runtime.CallWith(SourceAgent)
    → r.authorize() → permission.Service
         Policy = settings.permissionPolicy
         Gate   = ChannelGate → permission:ask / PermissionModal
    → 路径 / session / 超时 / 取消 / 执行

外部 MCP（NodeShell --mcp）
  MCP client → tools/call
    → 参数校验、工具白名单
    → r.authorize()          ← 仅用户确认层
         mcpPermissionMode=external（默认）：Auth=nil，不构造 NativeGate
         mcpPermissionMode=local：NativeGate + PolicyAsk
    → session / 路径 / host key / 限额 / 超时 / 取消 / 执行
```

GUI 与 MCP 是两个进程。MCP 不读取 GUI 内存中的 allow-session；local 模式固定 PolicyAsk，也不继承 GUI 的 `permissionPolicy=allow`。

---

## 2. external / local 行为

| 模式 | 用户确认 | NativeGate | 安全边界 |
|---|---|---|---|
| `external`（默认） | 由外部 MCP 客户端负责 | 不创建 | 全部保留 |
| `local` | NodeShell 系统对话框 | 创建，每次询问 | 全部保留 |

未知 `mcpPermissionMode`（例如 `"disabled"`）**回退到 `local`**（fail-closed，仍会弹本地确认），而不是变成无约束。缺省字段回退到 `external`。

---

## 3. 保留的安全边界

未删除、未因 Auth==nil 而跳过：

- 工具白名单（未知工具拒绝）
- 参数校验（必填、类型、timeoutMs 范围）
- `sessionId` 有效性（`SESSION_NOT_FOUND`）
- 本地路径 Home 守卫（upload/download）
- `acceptHostKey` 显式语义（默认不接受未知/变更密钥）
- MCP session 上限、idle reaper、busy 计数
- context cancel / timeout
- DisposeAll / SFTP handle 清理

`r.authorize()` 仍在敏感工具路径上调用；它只表示用户确认。注释已写明不得把它当成路径校验。

---

## 4. 公共 API

| 项 | 是否改变 |
|---|---|
| MCP 工具名、参数、返回结构、`tools/list` | **否** |
| `permission:ask` / `permission:closed` / `PermissionDecide` | **否**（GUI 仍用） |
| `settings.json` | **新增**可选字段 `mcpPermissionMode` |
| `window.api.settings` 形状 | **兼容扩展**（多一个可选字段） |
| NativeGate 代码 | **保留**，仅 local 模式装配 |

---

## 5. 配置迁移

- 缺少 `mcpPermissionMode` → `external`
- `"external"` / `"LOCAL"` → 对应模式
- 非法字符串 → `local`（fail-closed）
- 非字符串（如数字）按空值处理 → `external`
- `permissionPolicy` 仍只控制 GUI Agent，字段不删除

---

## 6. 修改文件清单

| 文件 | 说明 |
|---|---|
| `internal/permission/permission.go` | MCPMode、ParseMCPMode、NewMCPAuthorizer；consent vs safety 注释 |
| `internal/permission/native.go` | 仅 local 模式使用的注释 |
| `internal/permission/permission_test.go` | 模式解析、external 不建 Gate、与 GUI allow-session 隔离 |
| `internal/mcpcli/mcp.go` | RunMCP 按模式装配 Auth；external 不调用 newPermissionGate |
| `internal/mcpcli/runtime.go` | authorize 注释 |
| `internal/mcpcli/tools.go` | 敏感工具注释：确认层与安全执行分离 |
| `internal/mcpcli/tools_test.go` | nil Auth 仍执行；local 仍询问 |
| `internal/mcpcli/runmcp_test.go` | external 不构造 NativeGate；local 构造一次 |
| `internal/settings/settings.go` | `mcpPermissionMode` 持久化与规范化 |
| `internal/settings/settings_test.go` | 缺省/非法值/Set |
| `src/shared/types.ts` | `McpPermissionMode` |
| `src/renderer/src/App.tsx` | 设置状态与保存 |
| `src/renderer/src/components/SettingsModal.tsx` | MCP 页独立选项；GUI 权限文案拆开 |
| `src/renderer/src/i18n/locales/{en,zh}.json` | 中英文案 |
| `tests/ui/settings.test.tsx` | 默认 external、切换 local、GUI 权限文案 |
| `reports/MCP权限外置化_实施与验收报告.md` | 本报告 |

未改 P2 终端相关未提交文件。

---

## 7. 测试结果

| 环节 | 结果 |
|---|---|
| `go test -race ./internal/permission ./internal/mcpcli ./internal/settings` | ✅ |
| `go test -race ./...` | ✅ 22 包，0 data race |
| `npm test` | ✅ **25 文件 / 286 项** |
| `tsc` web + test | ✅ |
| `npm run build:wails` | ✅ |
| `git diff --check` | ✅ 无 whitespace error |
| ESLint | 0 error（仓库原有 prettier warning） |

---

## 8. 未解决风险

1. NodeShell 无法验证外部 MCP 客户端是否真的问过用户。
2. 能启动本地 `--mcp` stdio 进程的程序即是调用方；安全边界必须保留。
3. 各客户端授权粒度不同（按工具记忆 / 每次询问 / 默认信任）。
4. 若未来做 TCP/HTTP MCP，不能沿用本次 stdio 信任模型。
5. `local` 模式若长期无人使用，可在后续版本单独评估删除。
