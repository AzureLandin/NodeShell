# xterm.js P0 优化：编程 Agent 实施计划

## 1. 交付目标

请在不更换终端引擎、不改变 SSH 协议和不改变现有业务功能的前提下，完成 NodeShell 终端 P0 稳定性与性能优化。

本次交付必须解决或验证以下问题：

- 多会话切换时终端实例、输出路由和终端内容保持正确。
- 高速 PTY 输出时前端不会因为重复 IPC/写入而产生无界的额外任务。
- 终端尺寸变化时，相同 `cols/rows` 不重复发送远程 resize。
- 侧栏、SFTP 面板、窗口和字体变化结束后，远程 PTY 能收到最终正确尺寸。
- 组件卸载、会话关闭和应用退出时没有残留 listener、RAF、timer 或 ResizeObserver。
- WebGL 不可用或 context loss 时仍能继续使用终端。

本报告是执行清单。除非遇到明确的编译或测试阻塞，不要扩大到 P1/P2 功能。

## 2. 当前基线与关键约束

当前终端实现：

- 每个会话由 `TerminalView` 创建一个独立的 xterm.js `Terminal`。
- 使用 `FitAddon`、`UnicodeGraphemesAddon` 和可选的 `WebglAddon`。
- 每个实例当前设置 `scrollback: 1000`。
- 后端 `outputBatcher` 当前约每 `12ms` 或 `48KB` 发送一批输出。
- 前端通过 `requestAnimationFrame` 合并输出后调用 `term.write`。
- `ResizeObserver` 变化后当前约延迟 `80ms` 执行 fit 和远程 resize。
- 多个 `TerminalView` 通过稳定的 `sessionId` 挂载，非激活终端使用 `display: none`，但实例不销毁。
- `useSessions` 按 `sessionId` 将后台输出路由到正确终端；未挂载时保留有限输出环。
- Go SSH PTY 当前请求类型为 `xterm-256color`。

必须保持：

- `TERM=xterm-256color` 和现有 PTY 行列参数语义。
- `window.api.sessions.write(sessionId, data)` 调用契约。
- `window.api.sessions.resize(sessionId, cols, rows)` 调用契约。
- `session:data` 事件中的 `sessionId` 路由语义。
- 多会话终端不因切换而重建或清屏。
- 现有复制、粘贴、清屏、右键菜单、主题、字体和 SFTP/Agent 功能。

## 3. 修改文件边界

### 必改或重点检查

- `src/renderer/src/components/TerminalView.tsx`
- `src/renderer/src/hooks/useSessions.ts`
- `internal/sessions/batcher.go`
- `internal/sessions/batcher_test.go`
- `tests/ui/session-terminal-persistence.test.tsx`
- `tests/ui/terminal-view-contextmenu.test.tsx`

### 按需要修改

- `src/renderer/src/components/SessionTabs.tsx`
- `src/renderer/src/App.css`
- `tests/ui/connect-flow.test.tsx`

### 禁止无关修改

- 不修改 `internal/sshclient` 的 PTY 类型、认证和窗口变化协议。
- 不修改 SFTP、端口映射、Agent、主机管理和传输中心逻辑。
- 不新增终端引擎、Ghostty 或新的第三方依赖。
- 不直接调整 `12ms`、`48KB` 或 `scrollback: 1000`，除非基准测试证明当前值是问题来源，并在报告中记录前后数据。
- 不用静默截断输出的方式掩盖队列积压。

## 4. 实施阶段一：建立基线

在代码改动前，先运行并记录：

```powershell
npm run typecheck
npm test
npm run build:wails
```

记录以下现象或数据：

- 两个及以上会话之间切换 20 次，终端是否清屏或串屏。
- 后台会话持续输出时，切换回来是否有丢失。
- 侧栏和 SFTP 面板展开/收起一次产生多少次远程 resize 调用。
- 窗口连续调整时的 resize 调用次数。
- 高速输出时键盘输入是否明显延迟。
- WebGL 正常、禁用 GPU、远程桌面三种环境的行为。

如果项目当前存在与本任务无关的测试失败，先记录失败命令和原始输出，不要修改无关代码。

## 5. 实施阶段二：resize 去重与稳定同步

### 5.1 修改 `TerminalView.tsx`

在每个 `TerminalView` 实例内增加最近一次已发送尺寸的 ref，例如：

```ts
const lastSentSizeRef = useRef<{ cols: number; rows: number } | null>(null)
```

将 fit 和远程 resize 集中到一个内部函数，逻辑要求：

1. 终端不可见时不执行远程 resize。
2. 执行 `fit.fit()` 后读取 `term.cols` 和 `term.rows`。
3. `cols`、`rows` 必须是正整数，否则不发送。
4. 当前尺寸与 `lastSentSizeRef` 完全相同时，不调用 `window.api.sessions.resize`。
5. 尺寸变化时先更新 ref，再异步调用 resize，避免同一批回调重复排队。
6. resize 调用失败只记录错误或按现有错误处理方式忽略，不能 dispose 终端。

建议的行为模型：

```text
ResizeObserver / visible / font change
            ↓
取消上一次 debounce
            ↓
等待稳定窗口 + requestAnimationFrame
            ↓
fit()
            ↓
尺寸未变化：结束
尺寸变化：sessions.resize()
```

### 5.2 首次打开和 Tab 切换

- 首次打开当前可见终端时允许发送一次初始尺寸，即使 ref 为空。
- Tab 切换到已有终端时执行一次快速 fit，但仍经过尺寸去重。
- 切换到不可见终端时不得触发该终端的远程 resize。
- 终端从 `display: none` 恢复后，至少等待一个可测量的 animation frame 再 fit。

### 5.3 布局变化

优先只在 `TerminalView.tsx` 中通过 debounce 和稳定帧收敛 resize，不要为了 P0 大规模改造侧栏布局。

如果实际验证发现 CSS 动画期间仍然发送大量中间尺寸，可采用以下最小改动之一：

- 将稳定窗口从当前 `80ms` 小幅提高到覆盖动画尾部的范围，并用基准数据证明。
- 在 `SessionTabs` 或布局层提供“布局变化结束”通知，通知终端执行一次最终 fit。
- 使用 `transitionend` 时必须过滤目标属性和目标元素，避免同一动画触发多次。

不得通过永久停止 ResizeObserver 或永久禁止 resize 来消除问题。

## 6. 实施阶段三：输出消费治理

### 6.1 保留现有两级合并

保留以下结构：

```text
Go outputBatcher
  → Wails session:data
  → 前端 requestAnimationFrame
  → xterm term.write
```

不要退回到每个 SSH read 或每个 Wails 事件直接调用 `term.write`。

### 6.2 前端输出状态

在 `TerminalView.tsx` 中检查并完善以下 ref 生命周期：

- pending 输出字符串或队列
- RAF id
- 组件 disposed 标记
- xterm write 任务状态，如实现了异步 drain

要求：

1. 同一帧最多调度一次 flush。
2. flush 开始时先取出当前 pending，再允许新的数据进入下一批。
3. 组件卸载后到达的数据不得再写入已 dispose 的终端。
4. 卸载时取消 RAF；正常关闭时按现有语义处理尾部输出。
5. 不要增加一个与 xterm 内部队列重复的大型无限队列。
6. 不要在没有产品决策、监控和测试的情况下截断正常输出。

### 6.3 积压观测

增加开发或测试环境可用的轻量指标，不记录终端文本内容：

- pending 峰值长度
- flush 次数
- 单次 flush 字节数
- flush 时间或 xterm 写入回调耗时
- 持续积压时长

生产环境如保留指标，只保留计数、大小和耗时，不输出命令内容、密码或 SSH 数据。

如果发现持续积压，先通过指标确认瓶颈位置，再决定是否调整批处理参数。不能以“限制队列长度并丢弃旧输出”作为默认修复。

### 6.4 Go `outputBatcher`

重点检查并测试，不要先改参数：

- stdout/stderr 并发 Add 时顺序和互斥行为。
- UTF-8 多字节字符跨 read chunk 时的尾部保留。
- 达到字节阈值时的 flush。
- 定时 flush 和阈值 flush 竞争时不重复发送。
- Close/Discard 与 Add 并发时不晚发事件。
- emit 阻塞或返回时是否会影响关闭路径。

只有测试或基准证明实现存在问题时才修改 `batcher.go`，修改后必须补充回归测试。

## 7. 实施阶段四：多会话生命周期核对

### 7.1 `SessionTabs.tsx`

确认以下结构保持不变：

- `TerminalView` 的 React `key` 使用稳定的 `session.sessionId`。
- 所有已建立会话的终端保持挂载。
- `visible` 只控制当前终端显示和 fit，不控制输出路由。
- 连接中的 pending session 不创建错误的终端实例。

如果为了性能想卸载后台终端，必须停止该方向；完整终端状态无法仅依靠当前有限输出环恢复，属于后续独立设计。

### 7.2 `useSessions.ts`

逐项确认：

- `session:data` 使用事件携带的 `sessionId` 查找 listener。
- 不使用 `activeSessionId` 作为输出路由依据。
- 有 listener 时直接投递到对应终端。
- 没有 listener 时只写入对应 session 的输出环。
- 删除、关闭和重连时只清理对应 session 的 listener/output ring。
- 后台输出不会改变当前激活会话的终端内容。

### 7.3 资源清理

`TerminalView` cleanup 必须覆盖：

- Wails data listener
- xterm `onData` disposable
- ResizeObserver
- resize debounce timer
- output RAF
- pending flush 状态
- xterm Terminal
- WebGL addon相关资源

关闭一个会话时，其他会话的终端实例、listener 和输出必须保持不变。

## 8. 实施阶段五：WebGL 与降级

保持当前“尝试 WebGL，失败后使用 DOM renderer”的策略：

- WebGL 加载失败不能阻止终端打开。
- WebGL context loss 后不能继续向已失效 addon 写入导致异常。
- 不要在每次渲染或每次 Tab 切换时重复创建 WebGL addon。
- DOM 降级模式下输入、输出、resize、复制粘贴仍然可用。
- 如增加 renderer 指标，只记录 `webgl` 或 `dom`，不记录终端内容。

优先使用已有 xterm.js API和当前错误处理方式，不引入自定义渲染器。

## 9. 测试实施清单

### 9.1 `TerminalView` 测试

补充 mock 能力，使测试可以控制：

- `term.cols` 和 `term.rows`
- `ResizeObserver` 回调
- `requestAnimationFrame`
- xterm `write` 是否被调用
- xterm dispose 状态

至少覆盖：

1. 首次显示发送一次有效尺寸。
2. 相同尺寸重复 ResizeObserver 回调只发送一次 resize。
3. 多次不同尺寸变化最终发送最后一个稳定尺寸。
4. 隐藏终端不发送 resize。
5. 重新显示终端后能够重新 fit。
6. 卸载后触发延迟回调不会写入或 resize。
7. 输出在一帧内合并，且不丢失测试输入的完整内容。
8. WebGL addon失败时仍能写入和交互。

### 9.2 `session-terminal-persistence.test.tsx`

保持并验证已有测试：

- 两个会话创建两个终端实例。
- 切换 Tab 不创建新实例、不 dispose 旧实例。
- 后台会话输出写入对应的后台终端。
- 关闭一个会话只 dispose 对应终端。
- 慢连接期间已建立终端不被销毁。

增加快速切换、后台连续输出和关闭/重连竞争场景。

### 9.3 `batcher_test.go`

补充或确认以下测试：

- 高频 Add 的完整输出顺序。
- stdout/stderr 交错输入不会重复或丢失。
- UTF-8 字符跨 chunk。
- 阈值 flush 和 timer flush。
- Close、Discard、Add 并发。
- Close 后 Add 不再 emit。
- timer 在 Close 后不会继续运行或发出事件。

必要时增加 `go test -race` 验证 batcher 和会话输出并发安全。

## 10. 高负载验证

在可用 SSH 测试环境中验证以下场景：

- `yes`
- 大量 ANSI 彩色输出
- 长时间日志滚动
- 项目编译输出
- 中文、Emoji、组合字符和宽字符混合输出
- 两个以上会话同时后台输出并反复切换
- 侧栏、SFTP 面板连续展开/收起
- 连续拖动窗口边界
- Windows WebView2 禁用 GPU、远程桌面和不同 DPI 缩放

观察并记录：

- 输出是否完整、有序、无串屏。
- 键盘输入是否被明显延迟。
- pending 峰值是否持续增长。
- resize 调用次数、重复调用次数和最终尺寸。
- WebGL/DOM renderer 类型。
- 终端和应用内存是否持续无界增长。

## 11. 验收标准

### 必须通过

- `npm run typecheck` 通过。
- `npm test` 通过。
- `npm run build:wails` 通过。
- 现有多会话持久性测试全部通过。
- 相同 `cols/rows` 不产生重复远程 resize。
- 每次布局变化结束后，远程端最终收到正确尺寸。
- 测试能证明卸载后没有晚到的输出写入、resize 或资源回调。
- WebGL 不可用时终端仍可正常工作。

### 功能行为不能回归

- 输入仍发送到原 session。
- ANSI 控制序列和 UTF-8 字符显示不被批处理破坏。
- 复制、粘贴、清屏、主题切换和字体调整正常。
- 多会话切换不清屏、不串屏、不丢失已有终端实例。
- 会话关闭、重连和应用退出无残留资源。

### 性能判定

- 普通输出下 pending 队列能持续排空。
- 高速输出下界面仍能响应键盘和 Tab 切换。
- resize 不因侧栏/SFTP 动画产生无意义的重复调用。
- 长时间运行没有与输出量线性增长的额外 JS/Go 内存泄漏。

如果无法取得真实性能数据，至少提交可重复的测试场景、日志指标和前后对比，不要只写“手工感觉流畅”。

## 12. 提交内容

编程 Agent 完成后应提交：

1. 修改文件清单。
2. 每个修改点对应的原因和行为变化。
3. 是否调整过 `12ms`、`48KB` 或 `scrollback`，以及调整依据。
4. 测试命令和结果。
5. 高负载验证场景和关键指标。
6. 未完成项、已知限制和后续建议。
7. 明确确认未更换 Ghostty、未改变 SSH PTY 协议和未修改无关业务功能。

## 13. 回滚边界

如果高负载改动导致输入延迟、输出丢失、终端清屏或多会话回归，应优先回滚对应的输出队列改动，保留已经验证无副作用的 resize 去重和测试补充。不得通过删除多会话实例保持逻辑、缩短滚动历史或关闭 WebGL 来掩盖回归。
