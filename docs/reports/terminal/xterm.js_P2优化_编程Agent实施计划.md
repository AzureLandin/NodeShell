# xterm.js P2 性能与可维护性优化实施计划

## 1. 文档信息

- **项目**：NodeShell
- **阶段**：xterm.js P2 优化
- **执行对象**：编程 Agent
- **目标**：在 P0 稳定性优化已经完成的基础上，改善高吞吐、多会话场景下的终端性能、资源使用和可诊断性。
- **本阶段性质**：P2。重点是性能、工程质量和极端场景治理，不改变现有功能契约。

## 2. 背景与当前基线

P0 阶段已经完成以下基础治理：

- xterm.js 终端实例在会话切换时保持挂载，避免切换导致屏幕清空；
- resize IPC 去重、可见性判断和卸载清理；
- 前端输出 RAF 合并与 disposed 防护；
- Go 侧输出 batcher 的定时/阈值刷新、队列上限、背压及关闭清理；
- 多会话关闭、晚连接和并发场景的回归测试。

当前仍存在的工程改进空间：

1. batcher 的 sink 虽然已在锁外调用，但同步重入同一个 batcher 仍属于禁止用法；
2. 批处理参数固定，无法根据输出压力在延迟与吞吐之间自适应；
3. 缺少统一的终端输出、队列、IPC 和渲染指标；
4. 多会话、高速输出和长时间运行场景缺少系统化性能基准；
5. 后台会话仍需要保留终端内容，但应进一步减少不必要的布局和渲染开销。

## 3. 非目标与强约束

本阶段不得进行以下改动：

- 不替换 xterm.js，不引入 Ghostty；
- 不修改 SSH 协议、PTY 配置和 `xterm-256color`；
- 不修改 `sessions.write`、`sessions.resize`、`session:data` 等既有 API 契约；
- 不改变 SFTP、Agent、端口映射和会话管理的业务行为；
- 不通过丢弃正常终端输出换取性能；
- 不卸载后台会话的 xterm 核心实例，避免重新引入切换清屏问题；
- 不在本阶段加入暂停/继续等未确定需求；
- 不把性能指标、调试日志或内部状态暴露为不兼容的公共 API。

## 4. 实施原则

1. **先测量，再优化**：先建立基线和可重复的压力测试，再修改实现。
2. **保持输出完整性优先**：任何队列或调度优化都必须证明无丢失、无重复、顺序不变。
3. **关闭语义优先**：`Close` 必须保证已接受的数据按既有语义发送完成；`Discard` 只能用于明确的安静清理路径。
4. **分阶段提交**：每个阶段独立可编译、可测试，避免一次性重写 batcher 或 TerminalView。
5. **生产默认低噪声**：详细指标和调试日志默认关闭，仅通过诊断开关启用。

## 5. 分阶段实施内容

### P2-0：建立性能与正确性基线

#### 目标

在修改实现前记录当前版本的性能、队列和资源基线。

#### 任务

- 整理现有终端相关测试、batcher 测试和多会话持久化测试；
- 增加可重复的输出数据生成器，覆盖 ASCII、中文、emoji、ANSI 控制序列和分片 UTF-8；
- 建立 Go batcher benchmark，至少覆盖：
  - 小块低速输出；
  - 48 KiB 左右阈值输出；
  - 持续高速输出；
  - 慢 sink 和队列背压；
- 建立前端测试场景：
  - 单会话高速输出；
  - 2～8 个会话同时输出；
  - 输出期间快速切换会话；
  - 输出期间调整窗口、侧栏和 SFTP 面板；
- 记录测试机器、运行时版本和基线结果。

#### 预期产物

- 测试数据生成器；
- Go benchmark；
- 前端压力测试入口或测试辅助函数；
- 基线记录文档，不要求引入新的运行时依赖。

### P2-1：完善输出分发与重入安全

#### 目标

保留 batcher 的顺序、背压和关闭语义，同时明确处理 sink 阻塞和重入风险。

#### 任务

- 审查 `internal/sessions/batcher.go` 的状态机：`Add`、定时刷新、阈值刷新、`Close`、`Discard` 和 worker 退出路径；
- 不得重新引入持锁调用 sink；
- 评估并选择以下方案之一：
  1. 保持当前同步 sink 模型，并将“sink 不得同步重入同一 batcher”写入代码注释、测试和开发文档；或
  2. 增加独立的、仍然有界的事件分发层，使 sink 重入不会等待 worker 自身，同时定义清晰的排空、取消和错误语义。
- 如果采用方案 2，必须满足：
  - 队列仍有明确上限；
  - 不丢失已接受的正常输出；
  - 输出顺序不变；
  - `Close` 返回前完成需要完成的发送；
  - `Discard` 返回后没有晚发事件；
  - 不得因为 sink 永久阻塞而产生无法回收的 goroutine。
- 增加确定性测试，而不是只依赖 `Sleep`：
  - sink 锁外执行；
  - sink 慢速时 Add 的背压；
  - Close 等待在途发送；
  - Discard 唤醒等待者；
  - 关闭期间并发 Add 不丢数据；
  - 若实现支持重入，覆盖 Add/Close/Discard 重入；若不支持，测试和文档必须明确禁止。

#### 注意

禁止为了消除死锁而直接异步丢弃输出，也禁止用无限队列规避重入问题。

### P2-2：增加自适应批处理策略

#### 目标

降低低速场景延迟，并提升高速输出场景吞吐，避免固定参数在所有场景下都不合适。

#### 任务

- 先保留当前固定策略作为安全基线；
- 设计有限范围内的自适应参数：
  - flush 时间下限和上限；
  - 单批次字节数上限；
  - 队列容量上限；
  - 高负载时的合并策略；
- 仅根据内部队列长度、最近 sink 耗时和近期输入速率调整参数；
- 所有参数必须有硬上限，禁止无界增长；
- 低速交互输入不得因为自适应策略产生明显输入回显延迟；
- 高频输出不得阻塞关闭流程或造成内存持续增长；
- 保证 UTF-8 分片处理、ANSI 序列和输出顺序不受影响。

#### 验收要求

- 相同输入下，输出内容字节一致；
- 不得出现重复 chunk、缺失 chunk 或晚发事件；
- 低速输出延迟不劣于当前基线；
- 高速输出吞吐和队列峰值有可量化改善；
- 若实测收益不稳定，应保留固定策略并记录结论，不强行上线自适应算法。

### P2-3：优化 TerminalView 渲染与后台会话开销

#### 目标

在保持后台 xterm 实例和终端历史内容的前提下，减少无意义的布局、resize 和渲染工作。

#### 任务

- 审查 `src/renderer/src/components/TerminalView.tsx` 的：
  - xterm 实例初始化；
  - addon 初始化和销毁；
  - ResizeObserver、window resize 和可见性切换；
  - RAF 输出合并；
  - React effect 依赖和 cleanup；
- 确保后台会话：
  - 继续接收并保存输出；
  - 不因 `display: none` 触发无效 fit/resize；
  - 不重复初始化 xterm 或 addon；
  - 不重复注册 session:data 监听器；
- 评估 WebGL addon：
  - 正常启用路径不重复加载；
  - context loss 后只降级一次；
  - 降级后不会反复尝试导致抖动；
  - 卸载时完整释放相关资源；
- 对输出 RAF 调度增加明确的最大批量和调度边界，避免单帧无限写入阻塞 UI；
- 任何调度让步都必须保证剩余数据继续排队并最终写入。

#### 禁止事项

- 不因性能优化而清空 xterm buffer；
- 不在会话切换时重新创建终端实例；
- 不通过跳过后台输出导致返回会话时内容缺失。

### P2-4：增加可诊断指标

#### 目标

让终端卡顿、延迟、队列积压和 resize 抖动可以被定位，而不是依赖主观观察。

#### 任务

- 在 Go 侧增加内部指标，至少包括：
  - 接收字节数；
  - 发送 batch 数量和字节数；
  - 当前/峰值队列长度；
  - 队列等待次数和累计等待时间；
  - sink 调用次数和耗时；
  - Close/Discard 时状态；
- 在前端增加可选诊断指标，至少包括：
  - session:data 接收次数；
  - RAF 合并次数；
  - xterm.write 调用次数；
  - resize 触发次数和实际 IPC 次数；
  - WebGL 降级次数；
- 指标默认关闭或仅保留低成本计数，不得持续输出大量日志；
- 不记录终端正文、密码、密钥或敏感路径；
- 优先使用现有日志/诊断机制，不新增不必要的公共 API。

### P2-5：多会话资源治理

#### 目标

避免长时间运行和大量会话导致内存、监听器或 addon 泄漏。

#### 任务

- 增加多会话生命周期测试：创建、切换、关闭、重连、重复关闭；
- 统计并检查以下资源是否成对创建/释放：
  - xterm 实例；
  - Fit/WebGL/Unicode addon；
  - ResizeObserver；
  - window 事件监听器；
  - Wails session:data 监听器；
  - RAF 和 timer；
- 对终端历史 buffer 和超长输出制定可配置上限，但必须先确认产品是否允许截断历史；默认不得擅自改变用户可见历史行为；
- 如引入后台资源降级，只允许释放可重建的非核心资源，并验证恢复时不会清屏或丢输出。

### P2-6：压力测试与最终质量门禁

#### 必测场景

1. 单会话持续高速输出至少 5 分钟；
2. 8 个会话同时高速输出并快速切换；
3. 输出过程中频繁开关侧栏、SFTP 面板和窗口尺寸；
4. 输出、Close、Discard、重连同时发生；
5. 大量中文、emoji、ANSI 控制序列和跨 chunk UTF-8；
6. WebGL 正常、WebGL context loss 和 DOM fallback；
7. 慢 sink、异常 sink 和队列达到上限。

#### 自动化门禁

```powershell
gofmt -w internal/sessions/batcher.go internal/sessions/batcher_test.go internal/sessions/sessions.go internal/sessions/sessions_test.go
go test -race -count=1 ./internal/sessions
go test -race -count=1 ./...
npm test
npm run typecheck
npm run lint
npm run build:wails
git diff --check
```

如果新增 benchmark，应至少记录优化前后的结果；benchmark 不得替代 race test 和功能测试。

## 6. 推荐修改范围

优先允许修改：

- `E:\Projects\NodeShell\internal\sessions\batcher.go`
- `E:\Projects\NodeShell\internal\sessions\batcher_test.go`
- `E:\Projects\NodeShell\internal\sessions\sessions.go`
- `E:\Projects\NodeShell\internal\sessions\sessions_test.go`
- `E:\Projects\NodeShell\src\renderer\src\components\TerminalView.tsx`
- `E:\Projects\NodeShell\tests\ui\session-terminal-persistence.test.tsx`
- `E:\Projects\NodeShell\tests\ui\terminal-view-resize.test.tsx`
- 与指标或 benchmark 直接相关的新测试/诊断文件。

若需要修改其他文件，必须在提交说明中解释原因，并证明没有改变 SSH、PTY、SFTP、Agent 或公共 API 契约。

## 7. 编程 Agent 交付要求

完成后必须提供：

1. 修改文件清单；
2. 每个阶段的实现摘要；
3. 是否改变 API、SSH、PTY、SFTP 或 Agent 行为的明确说明；
4. 性能基线与优化后数据；
5. 自动化测试和压力测试的完整结果；
6. 未解决风险和后续建议；
7. `git diff --check` 结果。

## 8. P2 验收标准

### 正确性

- 多会话切换不会清屏、串流或丢失后台输出；
- 输出顺序、UTF-8、ANSI 控制序列保持正确；
- Close/Discard 没有晚发事件或 worker 泄漏；
- 队列始终有界，不存在隐式无限增长。

### 性能

- 低速交互输出没有明显新增延迟；
- 高速输出场景的 IPC/写入次数或 CPU 占用有可量化改善；
- 多会话切换和布局变化不会产生大量重复 resize；
- 长时间压力测试期间内存没有持续线性增长。

### 可维护性

- batcher 状态转换和 sink 约束有代码注释与测试覆盖；
- 指标默认低开销且不泄露终端敏感内容；
- 新增测试不依赖不稳定的固定 sleep，或已通过足够大的超时和同步信号保证确定性；
- 全部质量门禁通过。

## 9. 实施建议

推荐按 **P2-0 → P2-1 → P2-3 → P2-4 → P2-5 → P2-6** 顺序实施。P2-2 自适应批处理风险最高，应在有基线数据后再决定是否落地，不建议一开始就重写现有 batcher。

本计划的核心原则是：

> 先保证输出正确和生命周期安全，再用指标证明性能问题，最后对确实有收益的路径做局部优化。
