# NodeShell 多会话切换导致终端清屏：Bug 定位与修复建议

- **报告日期**：2026-08-25
- **问题状态**：已确认
- **严重程度**：高（破坏多会话终端的核心使用体验，但 SSH 连接本身通常未断开）
- **影响范围**：React/Wails GUI 的多会话终端视图；Go SSH 后端不是本问题的直接根因
- **建议修复位置**：`src/renderer/src/components/SessionTabs.tsx`，并补充 UI 回归测试

## 1. 问题摘要

打开两个或更多 SSH 会话后，在会话标签之间切换，之前会话中已经显示的命令输出、提示符、全屏 TUI 画面和 scrollback 会消失，表现为终端“清屏”。

该问题不是远端 shell 主动执行了 `clear`，也不是切换标签时调用了后端断开接口，而是前端在切换活动会话时**卸载并销毁了原会话的 xterm 实例**。再次切回时创建的是一个全新的空 xterm 实例；当前的后台输出 ring buffer 只记录终端卸载之后新到达的数据，无法恢复卸载前已经存在于 xterm 内存中的屏幕与滚动缓冲。

## 2. 可复现步骤

1. 启动 NodeShell。
2. 连接主机 A，执行若干会产生明显输出的命令，例如：
   ```bash
   pwd
   ls -la
   echo session-a-marker
   ```
3. 再连接主机 B，使 B 成为活动标签。
4. 在 B 中执行任意命令。
5. 点击主机 A 的会话标签切回 A。

### 实际结果

A 原先已显示的内容消失；若 A 在后台没有产生新输出，切回后通常是空白终端。若后台有少量新输出，可能只看到切走之后的新片段，而看不到切走之前的历史内容。

### 期望结果

切换标签只改变哪个终端可见，不应销毁会话 A 的 xterm 实例。切回 A 后应完整保留：

- 当前主屏/备用屏内容；
- scrollback 历史；
- 光标位置和终端解析器状态；
- 选择区域及终端内部状态（除非产品另有明确设计）；
- 切走期间持续到达的远端输出。

## 3. 根因结论

### 3.1 `SessionTabs` 只渲染当前活动会话的 `TerminalView`

当前代码位于 `src/renderer/src/components/SessionTabs.tsx:176-188`：

```tsx
<div className="session-terminals">
  {activeSession && activeSession.status !== 'connecting' && (
    <TerminalView
      key={activeSession.sessionId}
      sessionId={activeSession.sessionId}
      registerDataListener={registerDataListener}
      visible
      ...
    />
  )}
</div>
```

这里没有为 `sessions` 中的每个真实会话保留一个终端视图，而是只创建 `activeSession` 对应的一个 `TerminalView`。

当 `activeSessionId` 从 A 变为 B 时：

1. React 看到唯一子节点的 `key` 从 A 的 session ID 变成 B 的 session ID；
2. A 的 `TerminalView` 被卸载；
3. B 的 `TerminalView` 被重新创建；
4. 切回 A 时，又重复卸载 B、重新创建 A。

即使去掉 `key`，`TerminalView` 初始化 effect 依赖 `sessionId`，session ID 变化仍会清理旧 xterm 并初始化新 xterm，因此仅删除 `key` 不能正确修复。

### 3.2 `TerminalView` 卸载时明确销毁 xterm

`src/renderer/src/components/TerminalView.tsx:67-157` 中的 effect 创建 xterm；cleanup 中执行：

```tsx
return () => {
  unsub()
  onData.dispose()
  ro.disconnect()
  ...
  term.dispose()
  termRef.current = null
  fitRef.current = null
}
```

`term.dispose()` 会释放该 xterm 实例。终端已经解析出来的屏幕缓冲、scrollback、光标和 VT 状态都只存在于该实例中，销毁后无法从新实例自动恢复。

因此标签切换的实质不是“隐藏 A、显示 B”，而是“销毁 A、创建 B”。

### 3.3 现有 output ring 不是终端历史快照

`src/renderer/src/hooks/useSessions.ts:33-34` 定义了 96 KiB 的后台输出 ring；`useSessions.ts:89-98` 的逻辑为：

```tsx
const listener = dataListenersRef.current.get(sessionId)
if (listener) {
  listener(data)
  return
}
const prev = outputRingsRef.current.get(sessionId) ?? ''
outputRingsRef.current.set(sessionId, appendRing(prev, data, OUTPUT_RING_MAX))
```

`useSessions.ts:120-130` 在新 `TerminalView` 注册监听器时，只重放 ring 中的数据。

该 ring 的语义是：**终端没有监听器期间新收到的原始输出**。它不包含：

- 切换前已经写入 xterm 的内容；
- xterm 当前主屏/备用屏的完整快照；
- 光标位置、颜色、模式等 VT 解析状态；
- 被 96 KiB 截断之前的历史。

因此它只能避免“终端卸载期间的新数据完全丢失”，不能恢复被销毁的终端页面。若会话 A 在后台安静，没有新数据进入 ring，切回 A 时新终端自然是空白。

### 3.4 打开新连接或重连时也可能提前销毁已有终端

`SessionTabs.tsx:136-156` 在当前活动项处于 `connecting` 状态时，直接渲染连接中页面；包含 `.session-terminals` 的分支完全不挂载。

这会产生两个关联场景：

1. 已有会话 A 时创建新会话 B：B 的 pending tab 成为活动项并进入 `connecting` 分支，A 的终端容器会立即卸载；即使用户尚未完成 B 的连接，A 的 xterm 状态已经丢失。
2. 对已有真实会话执行 reconnect：`useSessions` 会暂时把原 session 标成 `connecting`，当前结构同样会卸载它的终端。即便修复普通标签切换，如果仍把终端列表放在 `connecting` 分支之外，重连期间仍会破坏终端状态。

因此修复不能只把活动终端改成 `sessions.map(...)` 后仍保留在当前 ternary 的非 connecting 分支中；**终端容器本身必须在连接中 UI 出现时继续挂载**。

## 4. 完整触发链

```text
用户点击另一个会话标签
  -> App 将 activeSessionId 设置为新 session ID
  -> SessionTabs 只渲染新的 activeSession
  -> React 卸载旧 TerminalView
  -> TerminalView cleanup:
       unsubscribe 数据监听
       dispose xterm
       清空 termRef / fitRef
  -> 旧终端的屏幕和 scrollback 永久丢失
  -> 新活动会话创建新的 xterm

用户切回旧标签
  -> 再次创建一个全新 xterm
  -> registerDataListener 只重放旧终端卸载后收到的 ring 数据
  -> 卸载前的终端页面无法恢复
  -> 用户看到空白或只有少量后台新输出，感知为“清屏”
```

## 5. 为什么不是 Go/SSH 后端问题

从当前调用链看，普通标签切换只调用 `setActiveSessionId`，不会调用：

- `window.api.sessions.disconnect`；
- `SessionsDisconnect`；
- SSH session 的 `Close`。

SSH 会话通常仍然存活，后端也仍可继续发送 `session:data`。丢失的是前端 xterm 的渲染状态。因此不建议从 Go 后端增加“重新发送历史输出”来规避此问题。

## 6. 推荐修复方案

### 6.1 核心原则

**一个已创建的真实 SSH session ID 对应一个长期存活的 `TerminalView`/xterm 实例。**

- 标签切换：只切换 `visible`，不卸载组件；
- 关闭会话：从 `sessions` 删除后才卸载并 dispose；
- pending 初始连接：没有真实 SSH session，可不创建 xterm；
- reconnect 中的旧真实 session：在新连接成功替换 ID 前，旧终端应保持挂载；
- 新 session ID 替换旧 ID 后，才按新会话语义创建新的终端实例。

### 6.2 建议修改 `SessionTabs.tsx`

不要只渲染 `activeSession`。应为全部**非 pending 的真实会话**渲染终端，并通过 `visible` 控制显示：

```tsx
<div className="session-terminals">
  {sessions
    .filter((session) => !isPendingSessionId(session.sessionId))
    .map((session) => (
      <TerminalView
        key={session.sessionId}
        sessionId={session.sessionId}
        registerDataListener={registerDataListener}
        visible={
          session.sessionId === activeSessionId &&
          activeSession?.status !== 'connecting'
        }
        fontFamily={terminalFontFamily}
        fontSize={terminalFontSize}
        resolvedTheme={resolvedTheme}
        onFontSizeChange={onTerminalFontSizeChange}
      />
    ))}
</div>
```

说明：

- `isPendingSessionId` 已由 `src/renderer/src/hooks/useSessions.ts` 导出，可直接导入；
- 不建议单纯按 `status !== 'connecting'` 过滤，因为 reconnect 会把一个已有真实 session 暂时标记为 `connecting`，这会再次卸载旧终端；
- hidden terminal 使用现有 `visible={false}` 即可，`TerminalView` 已支持 `display: none`；
- `TerminalView` 的 visibility effect 在重新显示时会执行 `fit()` 并调用后端 resize。

### 6.3 调整连接中页面的结构，避免卸载终端列表

当前的外层 ternary 在 active session 为 connecting 时不会挂载 `.session-terminals`。建议改为：

1. 当 `sessions.length === 0` 时显示 placeholder；
2. 只要存在真实会话，`.session-terminals` 始终挂载；
3. connecting UI 作为同级覆盖层或条件层展示在终端区域上方；
4. reconnect 中可以隐藏当前终端，但不能卸载；初次 pending 连接则只显示 connecting UI。

伪结构：

```tsx
<div className="session-terminal-area">
  {sessions.length === 0 ? (
    <p className="main-placeholder">...</p>
  ) : (
    <>
      {showDisconnectedBanner && <SessionBanner />}

      <div className="session-terminals">
        {realSessions.map((session) => (
          <TerminalView
            key={session.sessionId}
            sessionId={session.sessionId}
            visible={session.sessionId === activeSessionId && !connecting}
            ...
          />
        ))}
      </div>

      {connecting && <ConnectingOverlayOrPanel />}
    </>
  )}
</div>
```

具体采用 overlay 还是占据内容区，可按现有视觉保持最小改动，但必须保证 `.session-terminals` 没有因连接状态被 React 移除。

### 6.4 现有 ring buffer 的处理建议

`outputRingsRef` 可以保留，作为以下情况的短暂兜底：

- session 刚建立但对应 `TerminalView` 尚未完成 mount；
- React 生命周期切换中的极短监听空窗；
- 错误边界或异常卸载后的有限数据保护。

不要把 ring 扩大为“完整终端历史重放”方案。原始字节流的重放无法可靠恢复备用屏、光标、终端模式和被截断的状态，且会增加内存和延迟。

## 7. 不推荐的修复方式

### 7.1 只删除 `key`

无效或不完整。`TerminalView` 的初始化 effect 依赖 `sessionId`；session ID 变化仍会执行 cleanup、`term.dispose()` 并创建新实例，而且同一个 React 组件复用不同 session 也容易造成输出串线。

### 7.2 把旧输出全文存入字符串并重放

不可靠。终端是状态机，不是普通日志文本：

- ANSI/VT 控制序列可能依赖此前状态；
- `vim`、`top`、`htop` 等使用备用屏；
- 光标移动和局部重绘无法由截断日志稳定复原；
- 长会话会带来高内存占用；
- 重放时可能闪烁且性能较差。

### 7.3 请求 Go 后端重新发送历史

后端发送的是流，不保存完整 xterm 解析状态。把终端 UI 快照职责放到 SSH 后端会扩大协议和状态复杂度，仍无法准确恢复 xterm 的所有内部状态。

### 7.4 切换时调用 `term.clear()`/`reset()` 后再补数据

这会固化而不是解决当前问题，且进一步破坏终端状态。

## 8. 建议新增回归测试

现有 `tests/ui/connect-flow.test.tsx:304-337` 只验证：

- tab 的 `aria-selected` 是否变化；
- 关闭 tab 是否调用 disconnect。

它没有检查 xterm 实例是否被销毁，因此无法捕获此 bug。

### 8.1 最关键测试：切换不 dispose

扩展 xterm mock，使每个 `MockTerminal` 实例可被测试访问，记录：

- `open`；
- `write`；
- `dispose`；
- 对应的容器/session。

测试流程：

1. 连接 A，取得 A 的 terminal instance；
2. 向 A 发送 `session:data`；
3. 连接 B，取得 B 的 terminal instance；
4. 断言 A instance 尚未调用 `dispose`；
5. 切回 A；
6. 断言 A 仍是原 instance，而不是第三个新 instance；
7. 断言 A 的已有写入仍属于该实例；
8. 关闭 A 后才断言 A `dispose` 恰好调用一次。

### 8.2 新连接 pending 状态不销毁已有终端

1. A 已连接并产生输出；
2. 开始连接 B，但让 `sessions.connect` Promise 暂不 resolve；
3. B pending tab 显示 connecting UI；
4. 断言 A terminal 没有 dispose；
5. B 成功后切回 A，仍复用原实例。

这个用例能防止修复者只改 `sessions.map`，却仍让外层 connecting ternary 卸载全部终端。

### 8.3 后台输出继续进入对应终端

1. A、B 都已挂载，B 为活动项；
2. 对 A 发出 `session:data`；
3. 断言数据写入 A 的 terminal，而不是 B；
4. 切回 A 后不需要从新 xterm/ring 重建。

### 8.4 关闭与资源释放

- 切换标签不调用 `dispose`；
- 真正关闭某个 session 时调用一次 `dispose`；
- 关闭一个 session 不影响其他 terminal instance；
- 每个 session 始终只有一个 data listener，避免重复输出。

### 8.5 可见性与 resize

- hidden terminal 不应因隐藏容器的 ResizeObserver 反复向后端发送错误尺寸；
- inactive -> active 时执行一次 `fit()` 和 `sessions.resize`；
- 多次切换后尺寸仍正确，终端不清屏。

## 9. 手工验收清单

修复后至少验证：

1. A、B 两会话分别输出不同 marker，来回切换 20 次，内容均保留；
2. A 在后台持续输出日志，B 前台操作；切回 A 后输出连续且没有重复；
3. A 运行 `top`/`htop`/`vim` 等备用屏程序，切到 B 再切回，画面和交互状态保留；
4. A 有大量 scrollback，切换后仍可向上滚动查看；
5. A 已连接时启动 B 的慢连接，B connecting 期间 A 的实例不被销毁；
6. reconnect 进入 connecting 状态时旧终端不会因为 UI 状态切换被提前 dispose；
7. 关闭 A 后 A 的资源释放，B 不受影响；
8. 修改终端字体、字号、主题后，所有已挂载终端正确更新；
9. SFTP、监控、Agent 面板随 active session 切换的现有行为不回归；
10. 运行前端 typecheck、lint 和 Vitest 全套测试。

## 10. 性能与内存评估

推荐方案会让每个打开的 GUI session 持有一个 xterm 实例，而不是只让活动标签持有实例。这是保持真实终端状态最直接、最可靠的方式。

风险是会话数很多时增加 DOM、WebGL 和 scrollback 内存。但当前每个 terminal 的 scrollback 已限制为 1000 行（`TerminalView.tsx:15-16, 74`），并且用户主动打开的会话本身已经消耗 SSH/SFTP 等资源。对于桌面 SSH 客户端，多标签各自保留终端实例属于合理模型。

如后续需要进一步限制资源，建议单独设计明确的 GUI 会话上限或 inactive renderer 策略，不应通过销毁终端且不保存状态的方式隐式节省资源。

## 11. 建议改动范围

### 必改

- `src/renderer/src/components/SessionTabs.tsx`
  - 从“只渲染 active terminal”改为“所有真实 session 的 terminal 长期挂载”；
  - `visible` 按 active session 控制；
  - connecting UI 不得卸载终端容器。

### 建议补测

- `tests/ui/connect-flow.test.tsx`，或新增：
  - `tests/ui/session-terminal-persistence.test.tsx`

### 通常无需修改

- `internal/sessions/*`
- `internal/sshclient/*`
- `src/renderer/src/hooks/useSessions.ts` 的整体事件模型
- `src/renderer/src/components/TerminalView.tsx` 的 xterm 生命周期主体

若实现中发现 hidden terminal 的 WebGL/canvas 在 `display:none` 下恢复异常，可在 `visible` effect 内补充 `refresh()` 或重新 fit，但不要以 dispose/recreate 作为常规切换方式。

## 12. 修复验收标准

满足以下条件可认为该 bug 已修复：

- 标签切换前后，同一 session ID 对应同一个 xterm 实例；
- 切换标签不会触发该 session 的 `TerminalView` cleanup 或 `term.dispose()`；
- 终端屏幕、scrollback 和备用屏状态在切换后保留；
- inactive session 的输出仍进入它自己的 xterm，且不重复、不串线；
- 只有关闭 session、session ID 被真正替换或整个应用卸载时才 dispose；
- 新增自动化测试在旧实现上失败、在修复后通过。

## 13. 最终结论

该 bug 的直接根因已经明确：**`SessionTabs` 把会话切换实现成了 `TerminalView`/xterm 实例替换，而不是多个终端实例之间的显隐切换。** `useSessions` 的 96 KiB ring 只能补偿卸载期间的新输出，无法恢复被销毁的既有终端状态，因此用户看到清屏。

建议修复 Agent 以“每个真实 session 保持一个挂载的 `TerminalView`，切换仅改变 `visible`”为核心进行修改，并特别处理 active pending/reconnecting 状态，确保 connecting UI 不再卸载所有终端。

## 14. 独立复核补充：精确文件链、CSS 依据与竞态边界

### 14.1 App 到 TerminalView 的精确触发链

1. `src/renderer/src/App.tsx:149-163` 调用 `useSessions()`，取得 `activeSessionId` 与 `setActiveSessionId`。
2. `src/renderer/src/App.tsx:651-653` 由 `sessions.find(...)` 计算当前活动会话及其连接状态。
3. `src/renderer/src/App.tsx:695-702` 把 `activeSessionId` 直接作为 `SessionTabs.onSelect` 的目标状态，并把 `registerDataListener` 传入终端树；普通 tab 点击不会调用 `disconnect`。
4. `src/renderer/src/components/SessionTabs.tsx:79-107` 的 tab 点击回调只执行 `onSelect(session.sessionId)`。
5. `src/renderer/src/components/SessionTabs.tsx:52-54` 重新计算 `activeSession`、`sftpConnected`、`connecting`；这会让同一轮 React 更新同时改变 tab 高亮和终端分支。
6. `src/renderer/src/components/SessionTabs.tsx:176-188` 只创建 active session 的 `TerminalView`，并把 `key` 设为 `activeSession.sessionId`。因此 A -> B 必然是 A 子树卸载、B 子树挂载。
7. `src/renderer/src/components/TerminalView.tsx:67-95` 在挂载 effect 中创建 `new Terminal(...)`、加载 addon、`open()` 并保存 `termRef`。
8. `src/renderer/src/components/TerminalView.tsx:97-110` 注册对应 session 的数据监听器；`src/renderer/src/components/TerminalView.tsx:111-113` 注册终端输入到对应 session 的写入回调。
9. A 被卸载时，`src/renderer/src/components/TerminalView.tsx:144-154` 取消监听、清理观察器、取消 RAF、flush pending，然后执行 `term.dispose()`。
10. B 挂载完成后，B 是新的 xterm 实例。切回 A 时重复同一过程，A 的旧屏幕和 scrollback 已经不存在。

### 14.2 CSS 不是根因，反而显示出原设计支持“多实例常驻 + 显隐切换”

- `src/renderer/src/App.css:418-423` 的 `.session-terminals` 是相对定位、可伸缩容器。
- `src/renderer/src/App.css:425-434` 的 `.terminal-view` 是绝对定位，多个终端可以叠放在同一内容区。
- `src/renderer/src/components/TerminalView.tsx:287-292` 通过 inline `display: block/none` 控制 `visible`，并未通过 React 条件渲染销毁实例。
- `src/renderer/src/components/TerminalView.tsx:115-131` 的 fit 调度只在 `visibleRef.current` 为真时执行；`src/renderer/src/components/TerminalView.tsx:180-193` 在重新显示时重新 fit 并同步 resize。这些代码与“所有 session 的 TerminalView 保持挂载、只改变 visible”的设计是一致的。

因此不应通过修改 CSS 或在切换时调用 `clear/reset` 处理；修复重点是恢复正确的 React 挂载模型。

### 14.3 Git 历史确认的回归位置

当前工作树的 `SessionTabs.tsx` 逻辑不是最初多标签实现的逻辑。`ee101dd`（`feat: multi-tab xterm sessions with SSH I/O`）的终端区域曾经使用 `sessions.map(...)`，为每个 session 保持一个 `TerminalView`，并用 `visible={session.sessionId === activeSessionId}` 控制显隐。

在 `a9c93fc`（`Improve connect UX, performance, and memory use.`，2026-07-20）中，该区域改成只渲染 `activeSession`；同一提交还引入了 `outputRingsRef` 作为未挂载终端期间的输出兜底。这个优化把终端实例数量降为 1，但也把“标签切换”变成了“销毁/重建 xterm”，从而引入当前清屏回归。该历史证据与当前代码行号共同确认：问题是前端渲染策略回归，不是 Go SSH 层的输出或连接逻辑回归。

### 14.4 竞态与边界条件

#### A. pending 新连接导致已有终端提前卸载

- `src/renderer/src/hooks/useSessions.ts:135-161` 创建 `pending-*` tab 并立即激活。
- `src/renderer/src/components/SessionTabs.tsx:52-54,138-156` 看到活动项为 `connecting` 后，整个连接中分支替代了终端列表。
- 所以已有 A 正在显示时开始连接 B，A 的 TerminalView 也会被卸载，即使 B 最终连接失败或用户取消。

修复 Agent 必须把 connecting UI 从终端列表的生命周期中拆开；不能只把 `activeSession` 改成 `sessions.map(...)`，却仍把 map 放在当前 `connecting ? ... : ...` 的非 connecting 分支内。

#### B. reconnect 的真实旧 session 也会经历 connecting

- `src/renderer/src/hooks/useSessions.ts:232-239` 先把原 session 标记为 `connecting`。
- `src/renderer/src/hooks/useSessions.ts:241-259` 新连接成功后才用新 session ID 替换旧项并激活新 ID。
- `src/renderer/src/App.tsx:509-557` 负责错误、host-key 确认和失败恢复。

因此真实旧 session 在“连接新 session 尚未成功”的窗口内仍然存在；推荐让旧 TerminalView 保持挂载但隐藏/覆盖，只有 session ID 真正替换时才销毁旧实例。初次连接的 `pending-*` 项没有真实 SSH session，可以不创建终端实例。

#### C. output ring 只能覆盖监听空窗，不能覆盖 xterm 销毁

- `src/renderer/src/hooks/useSessions.ts:89-98`：已有 listener 时直接 return，不写 ring；无 listener 时才累积 ring。
- `src/renderer/src/hooks/useSessions.ts:120-131`：重挂载时先回放 ring，再注册 listener。
- `src/renderer/src/hooks/useSessions.ts:49-53`：ring 按 JS 字符串长度截断到 96 KiB，可能从 ANSI 控制序列或 Unicode 字符边界中间截断。

这意味着切换后 ring 只包含“卸载之后新来的原始流”，不含卸载前的屏幕状态。若修复采用常驻 TerminalView，ring 应保留为 mount/事件空窗兜底，不应被扩展成完整终端快照机制。

#### D. 极短的注册顺序窗口

`registerDataListener` 在 `src/renderer/src/hooks/useSessions.ts:122-127` 中先删除并同步回调 buffered output，再执行 `dataListenersRef.current.set(sessionId, cb)`。在当前 Wails 事件通常异步投递的前提下，这不是本次 bug 的主要来源；但若未来某个事件源可以在回调期间同步重入，理论上可能发生“buffer 已删除、listener 尚未写入”的极短丢包窗口。修复本 bug 不需要依赖扩大 ring；如修复 Agent 重构监听器，建议先注册 listener，再以 session 级顺序安全地 drain buffered data。

#### E. cleanup 的 sessionId 级删除缺少身份校验

`src/renderer/src/hooks/useSessions.ts:127-130` 的 cleanup 无条件 `delete(sessionId)`。正常 React 生命周期中，旧实例 cleanup 通常先于同一 session 的新实例注册，因此当前切换主路径不会因此串线；但在快速重挂载、StrictMode effect 重放或未来引入并行视图时，旧 cleanup 理论上可能删除新 listener。若修复 Agent 改动挂载策略，建议将 listener 存储为带 token/identity 的记录，cleanup 仅在 map 中仍指向自身时删除。这是加固建议，不是当前清屏 bug 的直接根因。

#### F. pending/real session ID 替换是合法的重建边界

`src/renderer/src/hooks/useSessions.ts:163-180`（首次连接）和 `232-265`（reconnect）会把 UI 项的 session ID 替换成后端返回的新 ID。由于 `TerminalView` 当前 effect 依赖 `sessionId`（`TerminalView.tsx:157`），ID 替换时重建实例是预期行为；不要为了修复普通 tab 切换而强行复用不同后端 session ID 的同一个终端，否则可能把输入、输出和 resize 串到错误的 SSH session。

### 14.5 现有测试的确切盲区

- `tests/ui/connect-flow.test.tsx:304-337` 只验证 tab 的 `aria-selected` 和关闭时调用 `sessions.disconnect`，没有断言 `Terminal` 实例数量、实例 identity、`dispose` 或后台输出归属。
- `tests/ui/connect-flow.test.tsx:17-28` 的 MockTerminal 虽有 `write` 和 `dispose`，但没有导出实例列表，也没有可观察的 per-session 输出历史，因此旧实现可以在测试中“清屏”而不失败。
- `tests/ui/helpers.tsx:208-228` 已提供 `emitSessionEvent(...)`，可直接向真实 `useSessions` 事件分发链注入指定 session 的 data；修复测试应复用它，而不是绕过 hook 直接调用组件内部回调。
- `tests/ui/terminal-view-contextmenu.test.tsx:7-67` 只覆盖单个 TerminalView 的上下文菜单与 addon 初始化，不覆盖 SessionTabs 的多实例生命周期。

建议至少新增/扩展以下断言：

1. A、B 连接后，A/B 各只创建一次 Terminal；切换 A -> B -> A 不增加 Terminal 数量，且两者 `dispose` 均为 0。
2. 对 inactive A 发 `session:data` 时，数据写入 A 的 Terminal，不写入 B；切回 A 后仍是原实例。
3. 开始 pending B 的慢连接时，A 的 Terminal 不 dispose；取消或连接失败后 A 仍可切回且历史保留。
4. 关闭 A 后只 dispose A 一次，B 的实例不受影响。
5. reconnect 失败时，旧真实 session 的 Terminal 不因 connecting UI 被提前 dispose；连接成功替换 session ID 后，才允许按新 session 语义创建新实例。
6. 反复切换并触发 `visible` 变化时，active terminal 执行 fit/resize，inactive terminal 不向后端发送无效尺寸。

## 15. 交付修复 Agent 的最短结论

**直接根因**：`SessionTabs.tsx:176-188` 只渲染 active `TerminalView`，而 `TerminalView.tsx:144-154` 在 unmount 时 `term.dispose()`；tab 切换因此销毁旧 xterm，切回只能新建空实例。`useSessions.ts:89-98,120-131` 的 output ring 只缓存卸载期间的新流，不能恢复原 scrollback。

**推荐修复模型**：每个真实 SSH session ID 长期保留一个 `TerminalView`/xterm 实例，tab 切换只改 `visible`；connecting UI 单独作为覆盖层/同级内容，不能把终端列表从 React 树中移除；pending 初始连接可不创建终端，真实 reconnect 旧 session 在新连接成功前保持挂载。

**验收核心**：A/B 来回切换 20 次，实例 identity 不变、切换不 `dispose`、后台输出进入正确实例；仅关闭 session 或真实 session ID 替换时释放实例。
