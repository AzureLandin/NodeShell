# xterm.js P2 性能与可维护性优化：实施与验收报告

- **实施日期**：2026-09-02
- **关联计划**：[xterm.js_P2优化_编程Agent实施计划.md](xterm.js_P2优化_编程Agent实施计划.md)
- **基线数据**：[xterm.js_P2优化_性能基线.md](xterm.js_P2优化_性能基线.md)
- **状态**：**正式验收通过 (PASS)**（含 96KiB 单批硬上限修复）

---

## 1. 实施前决策（计划中允许 Agent 自行选择的点）

| 议题 | 选择 | 理由 |
|---|---|---|
| P2-1 sink 模型 | **方案 1**：保持同步 sink，禁止重入 | 生产 Wails `EventsEmit` 单向派发；方案 2 再加一层有界队列仍会在 sink 永久阻塞时卡住 worker，且容易变成丢数据或无限队列 |
| P2-2 自适应批处理 | **启用，但只在队列积压时放大合并** | 慢 sink 基准 +32% 吞吐；队列未积压时仍是 12ms / 48KiB |
| P2-5 历史截断 | **不改 `scrollback: 1000`** | 计划要求先确认产品是否允许截断可见历史；默认不得擅自改变 |
| P2-6「5 分钟高速输出」 | **短自动化测试 + 可手动加长的 Go benchmark** | CI 单包超时 90s，5 分钟浸泡不能进默认 `go test` |

---

## 2. 各阶段实现摘要

### P2-0 基线

- 增加可重复语料：ASCII、中文、emoji、ANSI CSI、跨 Add 的 UTF-8 分片（`batcher_testdata_test.go`、`tests/ui/terminal-stress.ts`）。
- Go benchmark 覆盖小块低速、48KiB 阈值、持续高速、慢 sink 背压、混合语料，以及自适应对照。
- 前端压力入口：8 会话同时输出并切换、超大帧分片写入、后台会话继续写。

### P2-1 输出分发与重入安全

- 保持锁外调用 sink；用类型注释写明：**sink 不得同步调用同一 batcher 的 Add/Close/Discard**。
- 未引入第二层分发队列。
- 确定性测试（通道同步，而不是只靠 Sleep）：sink 锁外、慢 sink 背压、Close 等待在途发送、Discard 唤醒排队的 Add、Close 前已接受字节不丢失。

### P2-2 自适应批处理

- 生产构造器 `newSessionBatcher`：定时器固定 12ms。
- 队列深度 ≥ 8 时线性提高 **flush 触发阈值**（最多 96KiB），队列容量仍为 64。
- **单批硬上限 96KiB**：`takeLocked` 每次最多取出 96KiB，并在 UTF-8 边界切分；`Add`、定时刷新和 `Close` 的最终 flush 都遵守该上限。单次超大 `Add` 或 Close 时的超大 pending 会拆成多个 batch。
- 相同输入下输出字节与固定策略一致（`TestBatcherAdaptiveMatchesFixedBytes`、`TestBatcherHardCapFixedAndAdaptiveNoLoss`）。
- 慢 sink 场景批次更少、吞吐更高。

### P2-3 TerminalView

- 每帧最多写入 `256KiB` UTF-16 码元，剩余数据排队到下一帧；卸载时一次刷完。
- 不在 surrogate pair 中间切开。
- WebGL context loss / 构造失败只降级一次，不重试加载。
- 后台会话继续 `term.write`，不发 resize；隐藏时取消 pending fit/retry timer。

### P2-4 指标

- Go：始终记录 recv/emit/queue peak/wait/sink 耗时；`NODESHELL_TERMINAL_METRICS=1` 时仅在 Close/Discard 打一行计数，不含终端正文。
- 前端：仅当测试或诊断设置了 `window.__TERMINAL_METRICS__` 时发布；默认不写 window。

### P2-5 多会话资源

- 8 会话同时输出无串流；卸载后 xterm dispose 与 data listener 成对释放。
- 远端关闭后重连只替换该会话实例；重复 `session:closed` 幂等。
- 不卸载后台 xterm，不改 scrollback。

### P2-6 门禁

见第 5 节。5 分钟浸泡请本地执行：

```powershell
go test -bench=BenchmarkBatcherSustainedHighRate -benchtime=5m ./internal/sessions
```

---

## 3. 修改文件清单

| 文件 | 性质 | 说明 |
|---|---|---|
| `internal/sessions/batcher.go` | 修改 | 自适应阈值、**takeLocked 96KiB 硬上限与 UTF-8 切分**、内部指标、sink 重入约束、生产构造器 |
| `internal/sessions/batcher_test.go` | 修改 | 语料完整性、指标、通道化背压/Close/Discard、自适应对照 |
| `internal/sessions/batcher_bench_test.go` | 新增 | P2-0/P2-2 基准 |
| `internal/sessions/batcher_testdata_test.go` | 新增 | 混合输出语料 |
| `internal/sessions/sessions.go` | 修改 | 会话改用 `newSessionBatcher` |
| `src/renderer/src/terminal-output.ts` | 新增 | 每帧写入上限与 UTF-16 安全切分 |
| `src/renderer/src/components/TerminalView.tsx` | 修改 | RAF 分片、WebGL 一次降级、诊断计数、隐藏时取消 fit |
| `tests/terminal-output.test.ts` | 新增 | `takeWriteChunk` 单元测试 |
| `tests/ui/terminal-stress.ts` | 新增 | 前端压力语料 |
| `tests/ui/terminal-view-resize.test.tsx` | 修改 | 大块分帧、WebGL 不重试、后台写入、8 会话、指标 |
| `tests/ui/session-terminal-persistence.test.tsx` | 修改 | 重连替换实例、重复关闭幂等 |
| `reports/xterm.js_P2优化_性能基线.md` | 新增 | 基准数字 |
| `reports/xterm.js_P2优化_实施与验收报告.md` | 新增 | 本报告 |

未修改 SSH/PTY 类型、`sessions.write` / `sessions.resize` / `session:data` 契约、SFTP、Agent、端口映射。

---

## 4. 契约影响

| 面 | 是否改变 |
|---|---|
| 公共 API（`window.api` / Wails bindings） | **否** |
| SSH / PTY / `xterm-256color` | **否** |
| SFTP / Agent / 端口映射 | **否** |
| 用户可见终端历史（scrollback 1000） | **否** |
| 输出完整性（顺序、UTF-8、ANSI） | **否**（有测试证明字节一致） |
| 内部批处理 | **是**：积压时触发阈值可到 96KiB；每一个 queued/emitted batch 硬上限 96KiB |

指标不是公共 API：Go 侧 unexported snapshot + 可选 stderr 一行；前端仅在已有 `__TERMINAL_METRICS__` 对象上写计数。

---

## 5. 质量门禁结果

| 环节 | 命令 | 结果 |
|---|---|---|
| sessions 竞态测试 | `go test -race -count=1 -timeout 90s ./internal/sessions` | ✅ PASS（8.774s） |
| 全量 Go 竞态测试 | `go test -race -count=1 -timeout 90s ./...` | ✅ 22 个有测试的包全部通过，0 data race |
| Go benchmark | `go test -bench=BenchmarkBatcher -benchmem -count=3 ...` | ✅ 见基线文档 |
| 前端测试 | `npm test` | ✅ **25/25 文件，284/284 项** |
| Typecheck | `npm run typecheck` | ✅ 0 错误 |
| ESLint | `npm run lint` | ✅ **0 errors**（仓库原有 prettier warning 仍在；P2 改动文件无新增 error） |
| 生产前端构建 | `npm run build:wails` | ✅ PASS |
| Whitespace | `git diff --check` | ✅ 无 whitespace error（仅 Windows LF→CRLF 提示） |

---

## 6. 未解决风险与后续建议

1. **5 分钟真人浸泡未在本机 GUI 跑完。** 自动化覆盖了高速输出完整性、8 会话串流、Close/Discard 竞态和 WebGL 降级。建议在真实 Wails 窗口对 `yes` / `cat` 大文件做一次 5 分钟观察内存。
2. **快 sink 微基准上自适应略慢。** 生产 sink 是 Wails IPC。若以后 IPC 变得极快，可以再对比一次后把 `adaptive` 关掉。
3. **后台 xterm 仍常驻。** 未做「隐藏时卸 WebGL、显示时重建」——重建有清屏/闪烁风险，P2 明确禁止为性能卸载核心实例。
4. **scrollback 仍为 1000。** 若产品确认可以截断更长历史，再做成可配置项。
5. **`NODESHELL_TERMINAL_METRICS=1` 打到 stderr。** MCP 模式不走 GUI batcher；不要在文档以外把它当成稳定接口。

---

## 7. 验收对照

### 正确性

- 多会话切换不清屏、不串流；后台输出保留。
- 混合语料与自适应/固定策略字节一致。
- Close 等待在途发送；Discard 之后无晚发；worker 在 `DisposeAll` 晚连接路径仍会退出（P0 测试保留）。
- 队列上限 64；每一个 emit batch 的原始长度 ≤ 96KiB（`takeLocked` 硬上限，覆盖单次超大 Add、累计 Add、UTF-8 边界、Close 超大 pending）。

### 性能

- 低速路径定时器仍为 12ms。
- 慢 sink 高速输出吞吐约 +32%。
- 隐藏会话不发 resize；相同尺寸去重仍在。
- 每帧写入有硬上限，避免单帧打爆 UI。

### 可维护性

- batcher 状态机与 sink 约束写在类型注释里，并有测试。
- 指标默认低开销，不记录终端正文。
- 新测试以通道同步为主，`waitFor` 带超时。
