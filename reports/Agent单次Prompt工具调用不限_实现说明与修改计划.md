# NodeShell Agent 实现说明与单次 Prompt 工具调用不限修改计划

- **报告日期**：2026-08-26
- **报告状态**：实施完成，全量测试通过
- **目标**：取消 NodeShell 对单次 Agent prompt 总工具调用次数的数字上限
- **建议语义**：单次 prompt 可持续进行任意多轮“模型响应 -> 工具执行”，直到模型完成、用户停止、会话关闭或发生外部错误
- **本次交付**：代码修改与自动化测试已全部完成

---

## 1. 结论摘要

当前代码中有两个值为 `8` 的不同限制：

| 限制 | 位置 | 实际语义 |
|---|---|---|
| `DefaultMaxTurns = 8` | `internal/agent/agent.go` | 一个 prompt 最多进行 8 次 LLM assistant turn；达到后整个运行报错结束 |
| `maxToolCallsPerTurn = 8` | `internal/agent/llm.go` | 一次 LLM 响应最多同时返回 8 个工具调用 |

用户通常看到的“最多调用 8 次工具”主要来自 `DefaultMaxTurns = 8`。模型常见行为是每轮只请求一个工具，因此第 8 个工具执行后，下一轮还未得到最终回答就触发：

```text
Agent stopped after 8 steps without finishing
```

推荐修改：

```text
取消单次 prompt 的总 assistant turn 上限
保留单次 LLM 响应最多 8 个并行工具调用的批次保护
```

修改后，一个 prompt 可以执行：

```text
第 1 轮：1~8 个工具
第 2 轮：1~8 个工具
...
第 N 轮：1~8 个工具
最终轮：模型返回无工具调用的答案
```

因此单次 prompt 的**工具调用总数不再有 NodeShell 数字上限**。保留的 `maxToolCallsPerTurn = 8` 只限制一次响应的批量大小，不限制整个 prompt 的累计工具次数。

## 2. 当前 Agent 功能的完整实现方式

### 2.1 前端入口：`AgentPanel`

主要文件：

```text
src/renderer/src/components/AgentPanel.tsx
src/renderer/src/api/adapter.ts
src/shared/types.ts
```

前端 Agent 面板按 SSH session 保存独立 UI 状态：

- `transcripts`：每个 session 的用户消息、助手文本、工具卡片和错误提示；
- `running`：每个 session 是否正在运行；
- `picks`：每个 session 当前选择的 provider/model；
- `genRef` / `runGenRef`：Clear 后丢弃旧运行的迟到事件。

发送 prompt 时，`AgentPanel.send()` 执行：

1. 校验当前有活动且已连接的 SSH session；
2. 防止同一 session 在已有 run 时重复发送；
3. 调用 `agent.status()` 重新读取 provider、model 和 key 状态；
4. 将用户消息写入前端 transcript；
5. 调用：

```ts
window.api.agent.prompt(sessionId, title, text, providerId, model)
```

6. 后续通过事件接收运行结果：

```text
agent:delta  -> 流式助手文本
agent:tool   -> 一次工具执行结果卡片
agent:error  -> 已接受运行中的错误
agent:done   -> 运行结束，包含 aborted 状态
```

前端还提供：

```text
AgentAbort -> 用户点击 Stop，取消当前运行
AgentClear -> 取消运行并清空该 session 的对话
```

### 2.2 Wails App 绑定与配置解析

主要文件：

```text
app.go
```

`App.AgentPrompt(...)` 不直接执行 LLM，而是：

1. 通过 `agentConfigFor(providerID, model)` 从 settings 查找 provider；
2. 验证 model 属于该 provider；
3. 从操作系统 Keyring 读取对应 API key；
4. 生成：

```go
agent.Config{
    BaseURL: provider.BaseURL,
    Model:   model,
    APIKey:  key,
}
```

5. 调用 `agent.Service.Prompt(...)`。

生产环境 Agent 在 `App.wireAgent(...)` 中创建：

```go
a.agent = agent.New(agent.Deps{
    Tools: &agent.BoundCaller{MCP: rt},
    Sink:  sink,
})
```

这里没有显式传入 `MaxTurns`，因此当前使用 `agent.New` 设置的默认值 `8`。

### 2.3 每个 SSH session 一份 conversation

主要文件：

```text
internal/agent/agent.go
```

`Service` 使用：

```go
convs map[string]*conversation
```

按 session ID 保存：

- 模型侧 transcript；
- 当前是否运行；
- 当前运行的 `context.CancelFunc`；
- 用于 shutdown/dispose 等待 goroutine 结束的 `done` channel。

同一个 SSH session 同时最多运行一个 Agent prompt。第二个 prompt 会被拒绝，避免两个 goroutine 同时修改同一 transcript。

SSH session 关闭时，`disposeSink` 调用 `agent.Dispose(sessionID)`：

- 取消 Agent run；
- 等待工具/请求退出；
- 删除 transcript；
- 防止重连后继承旧连接的 Agent 上下文。

应用退出时调用 `DisposeAll()`，先停止 Agent，再拆除 SSH session。

### 2.4 Prompt 的后台运行方式

`Service.Prompt(...)` 只执行前置校验和启动 goroutine：

- session ID 非空；
- prompt 非空；
- prompt 不超过 32 KiB；
- provider/model/key 已配置；
- 当前 session 没有正在运行的 prompt；
- Service 未进入 shutdown。

接受后：

```go
ctx, cancel := context.WithCancel(context.Background())
go s.run(ctx, cancel, sessionID, title, cfg, conversation)
```

Wails 的 `AgentPrompt` Promise 很快返回；真正的输出通过事件流进入前端。

### 2.5 Agent 循环

当前 `internal/agent/agent.go` 的核心逻辑等价于：

```go
for turn := 0; turn < s.maxTurns; turn++ {
    messages := snapshot(conversation)
    result := streamLLM(messages)
    appendAssistant(result)

    if result 没有工具调用 {
        return nil
    }

    for each toolCall {
        executeTool()
        emit agent:tool
        appendToolResult()
    }
}

return "Agent stopped after N steps without finishing"
```

一次 turn 表示一次 OpenAI-compatible chat completion 请求，不等于严格的一次工具调用。一个 turn 可以返回 0~8 个工具调用。

### 2.6 LLM 请求与流式协议

主要文件：

```text
internal/agent/llm.go
```

Agent 使用 OpenAI-compatible Chat Completions：

```text
POST {baseURL}/chat/completions
Authorization: Bearer {API key}
stream: true
tools: [...]
```

响应通过 SSE 读取：

- 文本 delta 立即转发为 `agent:delta`；
- tool call 的 ID、name、arguments 按 index 拼接；
- SSE 结束后形成一个 `streamResult`；
- malformed SSE chunk 会跳过；
- endpoint 错误会清理和脱敏后返回。

当前单次 LLM 请求仍有以下边界：

| 边界 | 当前值 |
|---|---:|
| SSE 单行 | 1 MiB |
| 单轮助手文本 | 256 KiB |
| 单个工具参数 | 64 KiB |
| 单轮工具调用数 | 8 |
| endpoint 错误体 | 4 KiB |
| 单次 LLM request timeout | 120 秒 |

### 2.7 可调用工具

主要文件：

```text
internal/agent/tools.go
internal/agent/bound.go
internal/mcpcli/
```

模型只能看到四个远端工具：

```text
bash        -> 远端非交互命令
sftp_list   -> 远端目录列表
sftp_read   -> 读取远端文本文件
sftp_write  -> 覆盖写入远端文本文件
```

Agent 没有：

- 本地文件系统工具；
- 创建/选择其他 SSH session 的工具；
- 子 Agent；
- Skills/插件；
- 任意 MCP 工具透传。

`BoundCaller` 会忽略模型提供的 `sessionId` 和 `localPath`，强制注入当前 UI tab 的真实 session ID，因此模型不能跳到其他主机或访问本地磁盘。

工具调用实际复用 `mcpcli.Runtime.CallWith(...)`，来源标记为：

```go
permission.SourceAgent
```

敏感工具会经过 permission service，根据 `ask / allow / deny` 策略决定是否执行。

### 2.8 工具执行与 transcript 保护

工具逐个顺序执行，不并行执行。

当前保护包括：

- bash 默认最长 60 秒；
- 模型提供的 timeout 只能缩短，不能超过默认上限；
- 单个工具结果最多向模型回传 16 KiB；
- SFTP 文本文件最大 512 KiB；
- 目录列表最多 200 项；
- transcript 最多回放 40 条 message；
- 截断时保持 tool call/tool result 协议可用；
- abort 或错误发生在工具批次中间时，`reconcile()` 为未执行调用补充错误结果，防止下一次请求因 tool call 未应答而被 provider 拒绝。

## 3. “不限”的准确含义

本计划中的“不限”定义为：

> NodeShell 不再用固定 assistant turn 数或累计工具调用数终止一个已接受的 prompt。

运行仍然会在以下情况结束：

1. 模型返回一个不包含工具调用的最终回答；
2. 用户点击 Stop，调用 `AgentAbort`；
3. 用户 Clear 当前对话；
4. SSH session 被关闭；
5. 应用退出；
6. 单次 LLM 请求超过 120 秒；
7. 单个 bash 工具超过 60 秒；
8. provider 返回错误、断网或 API key/model 无效；
9. 用户拒绝敏感工具权限；
10. provider 自身的 token、上下文、速率或费用限制触发。

因此“不限”不表示请求永远不会停止，也不表示绕过 provider 限制；它表示**不再因 NodeShell 的固定数字 8 而停止**。

## 4. 推荐修改方案

### 4.1 将 `MaxTurns = 0` 定义为不限

保留 `Deps.MaxTurns` 作为测试和嵌入场景的可选保护，但修改语义：

```text
MaxTurns == 0  -> 不限
MaxTurns > 0   -> 最多 N 个 assistant turn
MaxTurns < 0   -> 归一化为 0，或构造时拒绝（实施时二选一）
```

推荐选择“负数归一化为 0”，保持 `New(...)` 不返回 error 的现有 API。

当前代码：

```go
if d.MaxTurns <= 0 {
    d.MaxTurns = DefaultMaxTurns
}
```

建议改为：

```go
if d.MaxTurns < 0 {
    d.MaxTurns = 0
}
```

生产环境 `wireAgent` 不传 `MaxTurns`，零值自然表示不限，无需增加设置项或前端参数。

### 4.2 移除 `DefaultMaxTurns = 8`

删除或停用：

```go
DefaultMaxTurns = 8
```

同步修改注释，不再描述“一次 run 成本由固定轮数限制”。新的注释应说明：

- 默认不限制 turn 数；
- 成本和生命周期由用户 abort、session 生命周期、request/tool timeout 和 provider 限制控制；
- `MaxTurns > 0` 仅用于测试或调用方显式配置。

### 4.3 将循环改为默认无限、可选有限

建议结构：

```go
for turn := 0; ; turn++ {
    if s.maxTurns > 0 && turn >= s.maxTurns {
        return errf(
            apperror.Unknown,
            "Agent stopped after %d steps without finishing",
            s.maxTurns,
        )
    }

    if err := ctx.Err(); err != nil {
        return err
    }

    // stream -> append assistant -> execute tools
}
```

这样：

- 默认 `maxTurns == 0` 时没有数字终点；
- 现有测试仍可以传 `MaxTurns: 3` 验证有限模式；
- 每轮顶部继续检查 context，Stop/Dispose 可及时退出；
- 现有最终回答、错误和 DoneEvent 语义不变。

### 4.4 保留 `maxToolCallsPerTurn = 8`

本次建议**不删除** `internal/agent/llm.go` 的：

```go
maxToolCallsPerTurn = 8
```

理由：

1. 它不是单次 prompt 的累计限制；
2. 一个 prompt 可以执行任意多个 8-call 批次；
3. 工具当前顺序执行，一次返回过多调用会产生很长等待；
4. OpenAI tool protocol 要求每个 call 都有对应 result，超大批次会显著增加 transcript 内存；
5. 当前 `MaxHistoryMessages = 40` 假设单个工具批次规模有限；直接取消批次上限可能破坏 tool-call/result 完整性；
6. 保留批次保护仍满足“单次 prompt 总工具调用次数不限”。

示例：模型每轮请求 8 个工具，连续 20 轮，则一个 prompt 可执行 160 个工具，不受累计次数限制。

如果后续明确要求“同一个 LLM 响应也允许无限个并行 tool_calls”，必须另做 transcript 分组、聚合字节预算和超大工具批次处理，不能只删除常量判断。

### 4.5 不增加前端设置项

本需求是改变默认产品行为，不建议新增“最大工具次数”设置：

- 前端 `AgentPrompt` 参数保持不变；
- TypeScript API 类型保持不变；
- settings JSON 不新增字段；
- Wails binding 不变化；
- 用户通过现有 Stop 按钮控制长任务。

如果未来需要成本控制，可另行设计“单次运行预算”，但不应与本次“不限”混合。

## 5. 需要修改的文件

### 必改

```text
internal/agent/agent.go
internal/agent/agent_test.go
```

`agent.go`：

- 删除 `DefaultMaxTurns = 8`；
- 修改 `Deps.MaxTurns` 注释和零值语义；
- 修改 `New(...)` 默认行为；
- 修改 `loop(...)` 为默认无限循环；
- 更新包注释中“模型不会永久循环”的描述。

`agent_test.go`：

- 保留显式 `MaxTurns = 3` 的有限模式测试；
- 增加默认模式超过 8 轮后仍能完成的回归测试；
- 增加超过 8 轮后仍可 Abort 的测试。

### 通常无需修改

```text
app.go
src/renderer/src/components/AgentPanel.tsx
src/renderer/src/api/adapter.ts
src/shared/types.ts
internal/agent/llm.go
internal/agent/tools.go
internal/agent/bound.go
internal/permission/*
```

原因：生产 `wireAgent` 已使用 `Deps` 零值；修改零值语义后会自动获得不限行为。前端和事件协议也不需要变化。

## 6. 测试计划

### 6.1 超过 8 轮后正常完成

新增测试建议：

```text
TestDefaultRunCanExceedEightToolTurnsAndFinish
```

构造：

- 前 12 个 SSE turn 每轮返回一个 `bash` 工具；
- 第 13 个 turn 返回最终文本，不调用工具；
- `Deps.MaxTurns` 保持零值。

断言：

- 12 个工具全部执行；
- 收到最终文本；
- 没有 `agent:error`；
- 恰好一个 `agent:done`；
- `done.aborted == false`。

该测试在当前实现上应失败于第 8 轮，在修复后通过，是本需求最关键的回归测试。

### 6.2 保留可选有限模式

现有：

```text
TestTurnLimitEndsWithError
```

继续传入：

```go
d.MaxTurns = 3
```

断言仍然在 3 轮后产生可观察 error。这样测试环境仍能快速验证无限循环模型的异常路径。

建议将测试名改为：

```text
TestExplicitTurnLimitEndsWithError
```

突出该限制已从默认行为变成显式可选行为。

### 6.3 超过 8 轮后可以停止

新增测试建议：

```text
TestUnlimitedRunCanAbortAfterEightTurns
```

构造持续请求工具的 endpoint，在记录到至少 10 个工具调用后执行：

```go
svc.Abort("s1")
```

断言：

- run 退出；
- `agent:done.aborted == true`；
- 不产生 `agent:error`；
- 没有 goroutine 遗留；
- transcript 经 `reconcile()` 后下一次 prompt 仍可使用。

### 6.4 Session 关闭与应用退出

复用或扩展现有测试，确认长循环中：

- `Dispose(sessionID)` 会取消并等待 run；
- `DisposeAll()` 会取消所有 run；
- 关闭后不再发送 delta/tool/error；
- 每个 accepted prompt 仍恰好发出一个 DoneEvent。

### 6.5 完整验证命令

```powershell
go test ./internal/agent -count=1
go test .
go test ./...
npm run typecheck
npm test
```

本次预计没有前端代码改动，但完整前端测试用于确认事件协议和 UI 状态没有回归。

## 7. 验收标准

- [x] 默认 Agent run 不再在第 8 个 assistant turn 后报错；
- [x] 单次 prompt 可执行至少 12 个顺序工具调用并正常返回最终答案；
- [x] 单次 prompt 可以跨多个工具批次继续运行；
- [x] 代码中不再存在生产默认 `DefaultMaxTurns = 8`；
- [x] `MaxTurns == 0` 的语义明确为不限；
- [x] 显式 `MaxTurns > 0` 的测试/调用方限制仍有效；
- [x] Stop 在超过 8 轮后仍能立即取消；
- [x] Clear、session close 和 app shutdown 仍能取消并 join run；
- [x] 单次 LLM request timeout 和单个工具 timeout 仍有效；
- [x] permission ask/allow/deny 行为不变；
- [x] 每个 prompt 仍恰好产生一个 `agent:done`；
- [x] `go test ./internal/agent -count=1` 通过；
- [x] 全仓库相关测试无新增回归。

## 8. 风险说明

### 8.1 模型可能无限循环并持续产生费用

取消固定轮数后，一个持续调用工具的模型可以一直运行。现有 Stop 按钮可以取消，但如果用户不干预，运行会继续。

这是“不限”行为的直接结果。报告不建议偷偷保留其他累计数字上限，否则仍不满足需求。

### 8.2 每轮 timeout 不是整次 prompt timeout

`DefaultRequestTimeout = 120s` 只限制一次 LLM HTTP 请求。下一轮会创建新的 120 秒 context，所以整个 prompt 可以持续很久。

这符合不限目标，但实施 Agent 应在注释中说明，避免误认为整个 prompt 仍受 120 秒限制。

### 8.3 Provider 上下文仍有限

`MaxHistoryMessages = 40` 会丢弃较早消息。长工具循环不会因 NodeShell 次数停止，但模型可能逐渐看不到早期步骤。

本次不建议同时重构上下文压缩；如长任务质量不足，应另开任务实现 summary/compaction。

### 8.4 权限弹窗可能频繁出现

在 permission policy 为 `ask` 时，大量写入或命令调用可能反复请求权限。用户可以使用已有 `allow-session`，本次不修改权限策略。

### 8.5 不应顺手取消所有资源边界

以下限制必须保留：

- prompt 字节数；
- SSE 行和响应文本字节数；
- 单个工具参数字节数；
- 单个工具结果字节数；
- SFTP 文件大小；
- 命令和请求 timeout；
- permission gate；
- session 绑定；
- 单 session 单 run。

这些是资源和安全边界，不是累计工具次数限制。

## 9. 不推荐的修改方式

### 9.1 把 `DefaultMaxTurns` 改成一个很大的数字

例如 `1000000` 仍然是有限，不符合“不限”，还会产生计数溢出和误导性错误。

### 9.2 只把默认 8 改成 0，但保留 `<= 0 -> DefaultMaxTurns`

当前构造逻辑会把 0 再改回默认值，因此必须同步修改 `New(...)` 的零值语义。

### 9.3 直接删除 Stop、timeout 或 permission

这些不是导致 8 次上限的代码，删除后只会增加失控和安全风险。

### 9.4 直接删除 `maxToolCallsPerTurn`

这不是累计限制。直接删除会让单个 SSE 响应构造任意大的 tool call map，并可能破坏 40-message transcript 的协议完整性。

### 9.5 在前端循环重复调用 `AgentPrompt`

Agent 循环已经正确位于 Go 服务。前端重复发送会与“同 session 单 run”约束冲突，并把一个 prompt 拆成多个用户消息，破坏 transcript 语义。

## 10. 交给实施 Agent 的最短任务说明

请将 `internal/agent` 的单次 prompt 总 assistant turn 限制改为默认不限：

1. 删除生产默认 `DefaultMaxTurns = 8`；
2. 将 `Deps.MaxTurns == 0` 定义为不限，`> 0` 保留为测试/显式限制；
3. 将 `Service.loop` 改为默认无限循环，并在每轮继续检查 context；
4. 保留 `maxToolCallsPerTurn = 8`，它只限制一次模型响应的工具批次，不限制 prompt 累计调用数；
5. 新增默认模式连续 12 个工具 turn 后正常完成的测试；
6. 新增超过 8 轮后 Abort 仍有效的测试；
7. 保留显式 3-turn limit 测试；
8. 不修改前端 API、permission、session 绑定、请求 timeout 或工具 timeout；
9. 运行 `go test ./internal/agent -count=1`、`go test ./...`、`npm run typecheck` 和 `npm test`。

## 11. 最终结论

NodeShell Agent 是一个绑定当前 SSH session 的 OpenAI-compatible 流式工具循环。当前“8 次”主要是 `DefaultMaxTurns = 8` 导致的 assistant turn 上限，不是前端限制，也不是 MCP session 数量设置。

最小且可靠的修改是：

```text
默认 MaxTurns: 8 -> 0（0 表示不限）
循环: 固定 8 轮 -> 直到模型完成或外部取消/错误
单轮工具批次: 继续最多 8 个
单次 prompt 累计工具数: 不限
```

这样能满足单次 prompt 工具调用总数不限，同时保留当前已经验证过的会话隔离、权限控制、超时、资源上限、Abort/Clear 和 shutdown 安全性。
