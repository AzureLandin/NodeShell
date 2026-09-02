# MCP 权限外置化实施计划

- **项目**：NodeShell
- **计划日期**：2026-09-02
- **实施对象**：编程 Agent
- **目标**：将外部 MCP Agent 的“用户授权确认”交给外部 Agent 应用管理，NodeShell 不再对 MCP 敏感工具重复弹出本地权限确认框；同时保留 NodeShell 必须承担的参数校验、安全边界、路径保护、SSH 主机密钥校验、会话和资源治理。
- **建议默认模式**：`external`

---

## 1. 背景

当前 NodeShell 同时支持两类 Agent：

1. NodeShell 内置的 GUI Agent；
2. 通过 `NodeShell --mcp` 接入的外部 Agent，例如 Cursor、Claude Desktop、Codex 或其他 MCP 客户端。

当前 MCP 进程通过 `NativeGate` 弹出 Windows MessageBox 或其他平台原生确认框，对以下操作进行二次确认：

- 执行远程命令；
- 写入远程文件；
- 上传文件；
- 下载文件。

该模式存在职责重复问题：外部 Agent 通常已经拥有自己的工具授权策略和用户确认流程，NodeShell 再次弹窗会造成重复确认、授权状态割裂和交互体验不一致。

本计划将 MCP 的“用户是否允许本次工具调用”交给外部 Agent，但不删除 NodeShell 的安全执行边界。

---

## 2. 目标架构

```text
外部 Agent 应用
  ├─ 负责用户授权确认
  ├─ 负责允许/拒绝策略
  ├─ 负责按工具、项目、会话记忆授权
  └─ 负责展示用户交互界面
          │ MCP stdio
          ▼
NodeShell MCP Server
  ├─ 工具白名单
  ├─ 参数校验
  ├─ session 有效性校验
  ├─ SSH host key 校验
  ├─ 本地路径保护
  ├─ 会话数量限制
  ├─ 空闲超时与取消
  ├─ SFTP 安全约束
  ├─ 资源清理
  └─ SSH/SFTP 实际执行
```

核心职责划分：

| 能力 | 负责方 |
|---|---|
| 是否允许 Agent 调用工具 | 外部 Agent |
| 是否显示确认界面 | 外部 Agent |
| 是否记住授权 | 外部 Agent |
| 工具是否存在 | NodeShell |
| 参数是否有效 | NodeShell |
| 本地路径是否安全 | NodeShell |
| SSH 主机指纹是否可信 | NodeShell + 外部 Agent 显式参数 |
| session 是否有效 | NodeShell |
| 会话数和空闲回收 | NodeShell |
| SSH/SFTP 实际执行 | NodeShell |
| 超时、取消和资源释放 | NodeShell |

---

## 3. 非目标与强约束

本次实施不得：

- 修改 MCP 工具名称、参数结构和返回结构；
- 修改 `tools/list` 的工具定义契约；
- 修改 SSH 协议、PTY 配置或 `xterm-256color`；
- 修改 SFTP 路径保护和本地文件访问边界；
- 默认接受未知或变化的 SSH host key；
- 删除 GUI 内置 Agent 的权限确认机制；
- 删除 `permission:ask`、`permission:closed` 和 `PermissionDecide` 的 GUI 通道；
- 让 MCP 调用绕过 `sessionId`、超时、取消、会话数和资源清理；
- 通过删除检查的方式解决安全问题；
- 把“外部 Agent 负责用户授权”误实现成“NodeShell 完全信任任意本地进程”；
- 在本阶段引入 TCP/HTTP MCP 服务、远程身份认证或新的网络监听端口。

---

## 4. 当前实现位置

重点检查和修改以下文件：

- `E:\Projects\NodeShell\internal\permission\permission.go`
- `E:\Projects\NodeShell\internal\permission\native.go`
- `E:\Projects\NodeShell\internal\permission\native_windows.go`
- `E:\Projects\NodeShell\internal\permission\native_darwin.go`
- `E:\Projects\NodeShell\internal\permission\native_other.go`
- `E:\Projects\NodeShell\internal\permission\channel.go`
- `E:\Projects\NodeShell\internal\mcpcli\mcp.go`
- `E:\Projects\NodeShell\internal\mcpcli\runtime.go`
- `E:\Projects\NodeShell\internal\mcpcli\tools.go`
- `E:\Projects\NodeShell\internal\settings\settings.go`
- `E:\Projects\NodeShell\app.go`
- `E:\Projects\NodeShell\src\renderer\src\App.tsx`
- `E:\Projects\NodeShell\src\renderer\src\components\PermissionModal.tsx`
- 相关 Go/TypeScript 测试文件。

编程 Agent 必须先确认当前工作区状态和现有测试，不得覆盖其他未提交改动。

---

## 5. 实施阶段

### 阶段一：梳理并冻结现有权限职责

#### 任务

1. 确认 `permission.Service` 当前同时服务 GUI Agent 和 MCP。
2. 确认 `ChannelGate` 仅用于 GUI WebView 权限弹窗。
3. 确认 `NativeGate` 仅用于 `--mcp` 进程。
4. 列出所有 MCP 工具执行前的 `Authorize` 调用点。
5. 区分以下三种检查：
   - 用户授权确认；
   - NodeShell 安全边界；
   - 工具业务参数校验。
6. 为 MCP 外置权限模式补充代码注释，说明外部 Agent 负责用户授权，但 NodeShell 仍不绕过安全校验。

#### 交付物

- 权限调用链说明；
- MCP 工具安全检查清单；
- 明确保留和移除的逻辑列表。

---

### 阶段二：增加 MCP 权限模式

推荐增加明确的权限模式，而不是直接删除代码。

建议定义：

```text
external
local
```

含义：

| 模式 | MCP 行为 |
|---|---|
| `external` | 不弹 NodeShell 本地确认框，由外部 Agent 管理用户授权 |
| `local` | 使用 NodeShell `NativeGate` 进行本地确认，作为兼容和兜底模式 |

建议默认值：

```text
external
```

#### 实现要求

- 模式解析必须 fail-closed 或明确回退到安全模式；
- 未知模式不得静默启用一个未预期的高权限行为；
- 不建议新增 `disabled` 作为默认模式；如实现该模式，必须明确它只表示“不额外询问”，不表示绕过安全边界；
- `RunMCP()` 根据模式决定是否注入 `NativeGate`；
- GUI 的 `permissionPolicy` 不得影响 `external` MCP 模式下的外部 Agent 授权流程；
- 外部 MCP 默认不读取 GUI 内存中的 allow-session 授权；
- MCP 和 GUI Agent 的授权状态必须继续隔离。

#### 配置建议

如果当前设置结构适合扩展，新增字段：

```json
{
  "mcpPermissionMode": "external"
}
```

如果产品不希望增加用户设置，也可以将 MCP 固定为 external，但必须：

- 删除或停用 MCP `NativeGate` 的生产装配；
- 保留相关代码只作为兼容/测试实现，或在确认无引用后删除；
- 在代码和文档中明确 MCP 授权由外部客户端负责。

优先采用配置模式，便于兼容没有用户确认能力的 MCP 客户端。

---

### 阶段三：移除 MCP 的重复用户确认

#### 推荐改法

在 `internal/mcpcli/mcp.go` 中：

- `external` 模式不再创建生产 `NativeGate`；
- Runtime 的 `Auth` 可以为 nil，或者注入一个仅保留安全策略的实现；
- 不允许因为 `Auth == nil` 而跳过路径保护、参数校验、session 校验和业务错误处理。

在 `internal/mcpcli/runtime.go` 和 `internal/mcpcli/tools.go` 中：

- external 模式下不调用用户授权性质的 `r.authorize()`；
- 或将 `r.authorize()` 重命名/拆分为清晰的安全检查，避免未来误把它当作路径校验；
- 保持 `CallWith()` 的工具分发、参数解析和业务执行顺序；
- 授权移除后，工具必须仍然在实际执行前完成必要的安全校验。

禁止直接用以下粗暴方式修改：

```go
if r.auth == nil {
    return nil
}
```

然后让所有其他检查也失效。必须逐项确认安全边界仍然存在。

---

### 阶段四：保留 NodeShell 安全边界

以下控制必须继续有效。

#### 4.1 工具白名单

只有注册在 `Tools()` 中的工具可以被调用。

未知工具必须返回稳定的错误，不得动态执行内部方法。

#### 4.2 参数校验

继续校验：

- 必填参数；
- 参数类型；
- 字符串空值；
- `timeoutMs` 的正数和最大值；
- `sessionId`；
- 远程路径和本地路径；
- 可选参数的合法范围。

#### 4.3 本地路径保护

`sftp_upload` 和 `sftp_download` 必须继续经过本地路径边界检查。

不得因为移除 NativeGate 而允许：

- 读取任意本地文件；
- 写入任意本地目录；
- 路径穿越；
- 符号链接逃逸；
- 覆盖受保护文件。

#### 4.4 SSH host key

`connect_host` 的 `acceptHostKey` 仍必须保持显式语义：

- 默认不接受未知或变化的 host key；
- 只有调用方明确传入 `acceptHostKey: true` 时才允许记住指纹；
- 不把 host key 接受行为与普通工具权限混为一谈。

#### 4.5 Session 与资源治理

继续保留：

- 最大 MCP session 数；
- idle timeout；
- begin/end busy 计数；
- Reap；
- CancelConnect；
- DisposeAll；
- 连接失败和取消后的清理；
- SFTP handle 清理；
- session 关闭后的 metadata 清理。

#### 4.6 超时和取消

外部 Agent 取消调用时，NodeShell 必须继续遵守 context cancellation。

长命令、SFTP 操作和连接操作不得因为权限层移除而失去超时或取消能力。

---

### 阶段五：调整设置界面与文案

如果新增 `mcpPermissionMode` 用户设置：

- 在设置界面增加清晰说明；
- 默认选择 `external`；
- 说明“外部 MCP Agent 负责用户授权，NodeShell 仍保留路径、主机密钥、会话和参数安全检查”；
- 如果选择 `local`，说明每次敏感操作可能弹出 NodeShell 系统确认框；
- 不要把 GUI Agent 的 `permissionPolicy` 与 MCP 模式混在同一个无说明的选项中。

如果不新增设置：

- 修改 GUI 相关文案，明确当前权限策略只控制 NodeShell 内置 Agent；
- 文档说明外部 MCP 客户端负责其自身的工具确认。

设置变更必须保持：

- 持久化；
- 非法值安全回退；
- 旧配置兼容；
- 前后端类型一致。

---

### 阶段六：测试与回归

#### Go 单元测试

新增或修改测试覆盖：

1. `external` 模式下敏感 MCP 工具不会触发 NativeGate；
2. `local` 模式下敏感 MCP 工具仍能触发 NativeGate；
3. `list_hosts`、`list_sessions`、`sftp_list`、`sftp_read` 的行为不变；
4. `run_command`、`sftp_write`、`sftp_upload`、`sftp_download` 在 external 模式下仍能正确执行；
5. 参数错误仍然在执行前返回；
6. 未知工具仍然拒绝；
7. 本地路径越界仍然拒绝；
8. host key 未确认时仍然拒绝连接；
9. session 不存在时仍然返回 `SESSION_NOT_FOUND`；
10. timeout 和 context cancel 仍然生效；
11. MCP session limit 和 idle reaper 仍然生效；
12. DisposeAll 后不会残留 session、SFTP handle 或 goroutine；
13. external MCP 不会复用 GUI Agent 的 allow-session 授权；
14. local 模式的 NativeGate 取消、拒绝和确认行为保持正确。

#### 前端测试

如果新增设置：

- 默认显示 `external`；
- 切换 `external/local` 能保存；
- 非法配置回退；
- GUI Agent 权限弹窗仍正常；
- MCP external 模式不触发 GUI `permission:ask`。

如果不新增设置：

- 验证现有 GUI 权限弹窗没有被删除或改变；
- 验证 `PermissionDecide` 和 `permission:ask` 仍可用。

#### 集成测试

至少覆盖：

```text
MCP client → tools/call → external mode → 安全检查 → SSH/SFTP 执行
MCP client → tools/call → local mode → NativeGate → 安全检查 → SSH/SFTP 执行
```

确认两个模式的差异只有“用户授权确认层”，不是安全边界差异。

---

### 阶段七：文档和迁移

新增或更新文档说明：

- 外部 MCP Agent 负责用户授权；
- NodeShell 保留服务端安全边界；
- external/local 模式的区别；
- 哪些操作仍可能被 NodeShell 拒绝；
- `acceptHostKey` 是独立的 SSH 主机指纹机制；
- 本地路径仍受保护；
- MCP stdio 进程的信任边界是“能够启动该进程的本地程序”。

旧配置迁移：

- 缺少 `mcpPermissionMode` 时使用 `external`；
- 非法值不得默认为无约束模式；
- 原有 `permissionPolicy` 继续控制 GUI Agent；
- 不删除旧字段，除非确认没有兼容需求。

---

## 6. 建议实现方案

推荐方案如下：

```text
GUI Agent
  └─ 继续使用 ChannelGate + permissionPolicy + PermissionModal

外部 MCP Agent
  ├─ 默认 external：由外部客户端负责用户授权
  └─ 可选 local：NodeShell NativeGate 兼容模式

NodeShell 两条路径都保留：
  ├─ 工具白名单
  ├─ 参数校验
  ├─ 本地路径保护
  ├─ host key 校验
  ├─ session/idle/limit 管理
  ├─ timeout/cancel
  └─ 资源清理
```

不推荐直接删除整个 `internal/permission` 包，因为 GUI Agent 仍然需要它。

不推荐直接删除 `NativeGate`，除非已经明确决定永远不支持无内置授权能力的 MCP 客户端。

---

## 7. 验收标准

### 功能验收

- 默认 MCP external 模式不弹 NodeShell 原生权限框；
- 外部 Agent 可以正常调用全部既有 MCP 工具；
- MCP 工具名称、参数和返回结构不变；
- GUI 内置 Agent 权限弹窗继续正常工作；
- local 兼容模式仍可用（如果实现该模式）。

### 安全验收

- 未注册工具仍拒绝；
- 参数错误仍拒绝；
- session 不存在仍拒绝；
- 本地路径越界仍拒绝；
- host key 未确认仍拒绝；
- session 数量超限仍拒绝；
- context cancel 和 timeout 仍生效；
- 不会因为 `Auth == nil` 跳过业务安全检查；
- 权限移除不导致密码、API key、文件内容泄漏。

### 回归验收

- `permission:ask`、`permission:closed` 和 `PermissionDecide` 的 GUI 测试通过；
- MCP 工具测试全部通过；
- session、SFTP、路径保护测试全部通过；
- 无新增 data race；
- 无 goroutine 泄漏；
- 构建和类型检查通过。

---

## 8. 质量门禁

编程 Agent 完成后必须运行：

```powershell
gofmt -w internal/permission internal/mcpcli internal/settings

go test -race -count=1 ./internal/permission ./internal/mcpcli ./internal/settings

go test -race -count=1 ./...

npm test
npm run typecheck
npm run lint
npm run build:wails

git diff --check
```

如果项目脚本在 Windows 环境中出现 shell 包装问题，必须直接运行对应的 `tsc` 检查并记录实际结果，不得只根据脚本退出信息猜测。

---

## 9. 编程 Agent 交付要求

完成后必须提交：

1. 修改文件清单；
2. 当前权限调用链和新架构说明；
3. external/local 模式行为说明；
4. 明确列出保留的安全边界；
5. 明确说明是否修改公共 API；
6. Go、前端、集成和压力测试结果；
7. 配置迁移说明；
8. 未解决风险；
9. `git diff --check` 结果。

---

## 10. 剩余风险

1. 外部 MCP 客户端是否弹出授权框由客户端实现，NodeShell 无法验证外部 Agent 是否真的获得了用户确认。
2. 任何能够启动本地 stdio MCP 进程的程序，都可能成为调用方；因此 NodeShell 必须继续保留安全边界。
3. 不同 MCP 客户端对工具授权的粒度可能不同，有的按工具记忆，有的每次询问，有的可能默认信任。
4. 如果未来增加 TCP/HTTP MCP Server，需要单独设计身份认证和服务端授权，不能沿用本计划的 stdio 信任模型。
5. `local` 兼容模式如果长期没有使用，可以在后续版本中单独评估删除，但不建议在本次改动中贸然删除。

---

## 11. 推荐实施顺序

```text
阶段一：梳理权限职责
  ↓
阶段二：增加 external/local 模式
  ↓
阶段三：external 模式移除重复 NativeGate 确认
  ↓
阶段四：逐项验证 NodeShell 安全边界
  ↓
阶段五：设置与文案调整
  ↓
阶段六：单元、集成和回归测试
  ↓
阶段七：文档、迁移和最终门禁
```

核心原则：

> 外部 Agent 负责“用户是否允许”，NodeShell 负责“调用是否安全、参数是否合法、资源是否可控以及操作是否可以执行”。