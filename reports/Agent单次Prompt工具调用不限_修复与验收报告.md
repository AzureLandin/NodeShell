# NodeShell Agent 单次 Prompt 工具调用不限：修复与验收报告

- **报告日期**：2026-08-26
- **关联计划**：[Agent单次Prompt工具调用不限_实现说明与修改计划.md](file:///e:/Projects/NodeShell/reports/Agent%E5%8D%95%E6%AC%A1Prompt%E5%B7%A5%E5%85%B7%E8%B0%83%E7%94%A8%E4%B8%8D%E9%99%90_%E5%AE%9E%E7%8E%B0%E8%AF%B4%E6%98%8E%E4%B8%8E%E4%BF%AE%E6%94%B9%E8%AE%A1%E5%88%92.md)
- **修复状态**：已完成并通过全套前端与后端自动化测试验证（100% PASS）
- **影响组件**：后端 Agent 服务循环（`internal/agent`）

---

## 1. 问题背景与定位

在之前的版本中，用户发送单次 Agent Prompt 进行多步操作（例如复杂运维、多文件检查与执行）时，当工具调用达到 8 次后常会触发以下报错并中断运行：

```text
Agent stopped after 8 steps without finishing
```

经过代码排查：
- 限制来源于 `internal/agent/agent.go` 中的 `DefaultMaxTurns = 8` 常量；
- `Service.loop` 将循环固定为 `turn < s.maxTurns`；
- 每轮 LLM 请求执行工具后若未得到最终回答，累计达到第 8 轮就会强制抛出错误终止运行。

---

## 2. 修复方案与代码变更

### 2.1 取消生产环境 `DefaultMaxTurns = 8` 硬编码限制 ([`internal/agent/agent.go`](file:///e:/Projects/NodeShell/internal/agent/agent.go))

1. **移除常量**：彻底删除了 `DefaultMaxTurns = 8`。
2. **重定义 `MaxTurns` 语义**：
   - `Deps.MaxTurns == 0`（默认值）：表示**不限制 assistant turn / 工具调用轮数**，支持长任务持续执行直到模型给出最终回答；
   - `Deps.MaxTurns > 0`：显式指定最大轮数（供特定测试或受限场景使用）；
   - `Deps.MaxTurns < 0`：在 `New(...)` 中自动归一化为 `0`（不限）。
3. **改造主循环**：
   - 将 `loop` 改造为默认无限轮循环：
     ```go
     for turn := 0; ; turn++ {
         if s.maxTurns > 0 && turn >= s.maxTurns {
             return errf(apperror.Unknown, "Agent stopped after %d steps without finishing", s.maxTurns)
         }
         if err := ctx.Err(); err != nil {
             return err
         }
         // ... stream LLM -> append assistant -> execute tools
     }
     ```
   - 每轮顶部与工具执行前后严格检查 `ctx.Err()`，保证前端点击 Stop（`Abort`）、Clear 会话、关闭标签页（`Dispose`）或应用退出（`DisposeAll`）时能够即时取消。
4. **批次安全保护保持**：
   - 保留 `internal/agent/llm.go` 中的 `maxToolCallsPerTurn = 8`，仅限制单次 LLM HTTP 响应返回的并发工具批次大小，不限制整个 prompt 的累计调用总数。

---

### 2.2 新增与调整后端测试 ([`internal/agent/agent_test.go`](file:///e:/Projects/NodeShell/internal/agent/agent_test.go))

1. **新增长任务测试 `TestDefaultRunCanExceedEightToolTurnsAndFinish`**：
   - 构造连续 12 轮工具调用 + 第 13 轮返回最终文本的 SSE 序列；
   - 验证在默认零值（不限）配置下，12 个工具全部成功执行，收到最终文本，且无任何 `agent:error`，正常发出 `done.aborted == false`。
2. **新增长任务取消测试 `TestUnlimitedRunCanAbortAfterEightTurns`**：
   - 构造持续调用工具的无限序列，在第 10 轮时触发 `svc.Abort("s1")`；
   - 验证长循环能被干净中断，发出 `done.aborted == true`，无 goroutine 遗留且 transcript 协议正常对齐。
3. **调整显式限制测试 `TestExplicitTurnLimitEndsWithError`**：
   - 验证传入 `MaxTurns: 3` 时，仍能在达到 3 轮后准确报告 step limit 错误。

---

## 3. 全量测试与验证结果

| 验证环节 | 执行命令 | 结果 |
|---|---|---|
| **Agent 单元测试** | `go test ./internal/agent -count=1 -v` | ✅ **全部 29 项单元测试通过** |
| **Go 后端全量测试** | `go test -count=1 ./internal/...` | ✅ **20/20 packages 全部通过** |
| **全量前端测试** | `npm test` | ✅ **21 个套件 / 228 项测试全部通过** |
| **TypeScript 类型检查** | `npm run typecheck` | ✅ **0 errors** |
| **前端打包构建** | `npm run build:wails` | ✅ **成功构建** |

---

## 4. 人工核查与验收清单

- [ ] 给 Agent 发送需要多步执行的任务（例如“逐步排查系统日志、磁盘空间并整理为清单”），工具调用超过 8 次后不再报错 `Agent stopped after 8 steps without finishing`；
- [ ] Agent 在执行多轮工具后，能够正常输出最终总结文本并结束运行；
- [ ] 在多轮长任务执行过程中（例如第 10 轮时），点击 Agent 面板中的 **Stop** 按钮，能够立即停止并保留已生成的会话历史；
- [ ] 工具调用过程中的超时、权限询问、单工具 60 秒限制依然正常生效。
