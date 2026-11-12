# NodeShell 前端工程改造计划

日期：2026-11-12。状态：P1—P7 实现完成，P8 本机代码收尾和自动检查完成；macOS ARM64 原生构建、MCP smoke 和本地打包通过。P8 原生交互、Windows/Linux 和真实性能验收仍待完成，整份计划尚未通过全部发布门槛。详见[P8 验收记录](../reports/ui/frontend-p8-acceptance.md)。

## 1. 目标与范围

本次改造围绕五个问题展开：基础组件复用不完整、弹层交互职责重复、公共 UI 接口不统一、表单与异步状态重复管理、功能和样式边界过宽。

目标是让通用交互由现有成熟组件承担，让业务代码集中表达 SSH、SFTP、Agent 和设置流程。验收依据是职责是否清晰、交互是否可靠、变更影响范围是否可控，不以减少组件数量、原生标签数量或文件行数作为目标。

先完成不增加运行时依赖的改造，再分别试点表单和查询状态管理。每批变更应能独立构建、测试和回退；业务重构、目录迁移和视觉调整分开提交。

### 当前基线

以下为本次审查时的工作区状态，包含此前已完成的测试目录整理：

| 项目 | 当前情况 | 改造含义 |
| --- | --- | --- |
| 基础组件 | 已依赖 `@base-ui/react`，`ui/` 有 14 个组件封装文件 | 优先补齐现有体系的使用，避免再建立第二套体系 |
| 终端与编辑器 | 已使用 xterm 和 CodeMirror | 保留业务适配层，重点保护生命周期和事件行为 |
| 页面入口 | `App.tsx` 840 行，混合设置、连接流程和弹窗协调 | 按职责抽离，而非机械拆成更多渲染函数 |
| 样式入口 | `App.css` 4749 行，公共组件仍兼容旧选择器 | 按组件和功能逐步迁移样式所有权 |
| 测试 | `tests/unit/`、`tests/ui/`、`tests/contracts/`、`tests/support/` | 保留正式测试，扩展业务交互覆盖 |
| 构建 | 配置位于 `config/`，Vite 输出 `frontend/dist/` | 保持 Wails 构建与 Go 嵌入路径一致 |

本文引用的源码路径均相对 `src/renderer/src/`；测试和配置路径相对仓库根目录。基线与 P1—P6 记录保留当时路径；P7 已完成目录迁移，当前源码位置和维护规则见[前端结构与维护约定](frontend-structure.md)。

### 必须保持的应用契约

- 保持 Wails 运行方式、`window.api` / `ElectronApi` 命名、IPC DTO 和事件契约；新模块通过现有 API 适配层访问后端。
- 保持主机密钥验证、OS keyring 和本地路径校验流程；表单库不改变后端验证和凭据存储规则。
- 密码和 API Key 仅保留在完成交互所需的短期状态中，不放入通用查询缓存、持久化状态或调试日志。
- 保持终端在会话切换后的内容、连接、缓冲、尺寸调整和资源释放行为。
- 保持 MCP 工具、构建标志、版本来源和跨平台打包流程；本计划不要求修改 Go API。
- 保持现有中英文文案和明暗主题；改造发现的视觉变化应单独说明并验证。

## 2. 目标结构与依赖方向

建议最终结构如下。目录随对应功能迁移创建，不预建空目录，也不要求第一批整体搬迁。

```text
src/renderer/src/
  app/                    # 应用组合、providers、跨功能流程协调
  features/
    hosts/                # 主机管理、HostForm、主机查询
    sessions/             # 会话标签、终端适配、连接流程
    sftp/                 # 目录浏览、文件菜单、编辑器适配
    agent/                # 对话、模型配置、流式事件
    settings/             # 设置界面、读取和保存策略
    monitoring/           # 监控界面
    tunnels/              # 端口映射界面
    transfers/            # 传输中心和任务事件
    permissions/          # 后端权限请求队列与界面
  ui/                     # Base UI 封装、公共视觉变体、通用组合组件
  api/                    # 保留 Wails bridge / adapter
  i18n/
  theme.css               # 全局主题变量
  App.tsx                 # 保留入口，组合应用模块
  App.css                 # 逐步收敛为应用框架布局
```

依赖方向为 `App / app → features → ui、api、shared`。`ui` 不导入业务功能、`window.api` 或 SSH 配置类型；功能之间不直接导入对方内部组件和 hooks。跨功能动作由应用组合层通过明确的参数与回调协调，跨功能类型优先沿用现有共享契约。

功能入口只导出实际被其他模块使用的接口，不建立导出整个目录的总入口。迁移时同步更新源码、测试导入和文档；如使用路径别名，应先统一 Vite、Vitest 与 TypeScript 的解析规则。

## 3. 问题一：已有基础组件没有完整复用

### 现状与影响

`ui/tabs.tsx` 已封装 Base UI Tabs，但 `components/SidebarPanel.tsx` 和 `components/SessionTabs.tsx` 仍自行实现标签状态、ARIA 角色和键盘处理。会话标签每项都使用 `tabIndex={0}`，键盘逻辑主要处理 Enter / Space。

同类交互分别实现，会增加键盘导航、焦点恢复和可访问性维护成本。侧边栏切换与终端会话切换的生命周期要求不同，需要复用基础交互，同时保留各自业务规则。

### 解决方案

1. 将 `FormTabs` 系列提升为命名中性的公共 Tabs 接口，明确受控值、激活方式、方向、禁用和内容保留策略。先更新现有调用，再删除旧命名；兼容导出仅用于分批迁移，并在收尾阶段移除。
2. 先迁移侧边栏。业务层保留活动标签值，库承担键盘导航、焦点移动和标签与面板的关联。保持监控和端口面板现有挂载、隐藏及订阅语义。
3. 再迁移会话标签。会话 ID 作为稳定值，连接占位会话、重连替换、关闭活动项后的选中规则沿用现有业务逻辑。
4. 终端渲染容器继续按会话 ID 稳定挂载。不能因公共 Panel 默认行为而卸载终端；实施时核对已安装版本的挂载控制能力，必要时保留受控的持久内容宿主，并补齐标签与面板的可访问性关联。
5. 关闭动作使用独立可聚焦控件，避免在标签按钮内嵌套另一个按钮。明确鼠标关闭、键盘关闭后焦点及相邻会话选择规则。

### 影响范围与验收

主要修改 `ui/tabs.tsx`、`components/SidebarPanel.tsx`、`components/SessionTabs.tsx` 及现有 Tabs 调用点。

- 扩展 `tests/ui/session-terminal-persistence.test.tsx`：切换不重建 xterm、不重复订阅、不丢内容；关闭只释放目标终端。
- 新增侧边栏业务交互测试：方向键与首尾导航、选中和焦点关系、面板隐藏与事件订阅。
- 验证关闭首项、中间项、末项、活动项和最后一个会话；保留连接中及连接失败界面。
- 检查 tablist、tab、tabpanel 的关联和可访问名称，避免重复实现库内部测试。

## 4. 问题二：弹层交互存在多套状态和事件处理

### 现状与影响

`ui/context-menu.tsx` 已委托 Base UI 处理定位、导航与关闭，但 `SftpPanel`、`TerminalView` 仍注册全局鼠标和键盘关闭监听，两个业务菜单也保留内部 `open` 状态。生产事件处理还依赖菜单的 `data-testid`。

`ModalShell` 同时承担动画完成通知、关闭策略和嵌套 Escape 传播。这些策略具有应用意义，需要明确保留的规则，而不是直接删除全部自定义逻辑。

### 解决方案

1. 菜单状态采用单一来源：业务宿主持有“位置＋业务目标”或空值，菜单是否显示由其派生；公共菜单受控接收显示状态和关闭请求。退出动画如需要独立过渡状态，由公共层负责，不再建立第二份业务显示状态。
2. 通用的外部点击、Escape、键盘导航、焦点恢复交给公共菜单实现。先验证事件顺序，再移除业务组件中重复的全局关闭监听。
3. 终端仅保留右键目标、选择内容和终端键盘桥接等领域逻辑。菜单显示时 Escape 关闭菜单；菜单关闭后 Escape 正常进入远端 shell / vi。
4. 移除生产代码对 `data-testid` 的行为判断。确有业务事件边界时使用显式 ref 或具名公共接口，测试定位属性只服务测试。
5. 为菜单与弹窗明确关闭原因和收尾时机：关闭请求不等于退出动画完成；业务卸载、焦点恢复、父级关闭只执行一次。
6. 保留 `ModalShell` 的密码弹窗禁止外部点击、忙碌期关闭限制、嵌套 Escape 和 reduced-motion 策略。将其整理成公共组合组件的明确参数，不让每个业务弹窗复制实现。

### 影响范围与验收

主要修改 `ui/context-menu.tsx`、`components/SftpContextMenu.tsx`、`components/TerminalContextMenu.tsx`、`components/SftpPanel.tsx`、`components/TerminalView.tsx` 和 `components/ModalShell.tsx`。

- 扩展 `terminal-context-menu.test.tsx`、`terminal-view-contextmenu.test.tsx`、`sftp.test.tsx`，覆盖外部点击、Escape、动作后关闭、连续右键重新定位及禁用项。
- 验证菜单关闭后终端焦点恢复，以及正常 Escape 可以发送到远端；菜单按键不误发为终端命令。
- 沿用并扩展 `modal-shell-motion.test.tsx`、`permission-modal.test.tsx`、`connect-flow.test.tsx`，覆盖嵌套弹窗、密码弹窗、取消连接和动画关闭只通知一次。
- 在真实 Wails WebView 验证事件传播、焦点、剪贴板及窗口边缘定位；jsdom 结果不替代这些检查。

## 5. 问题三：公共 UI 接口和通知入口不统一

### 现状与影响

`ui/button.tsx` 主要透传底层 Props；`ui/dialog.tsx` 手工维护部分底层事件与状态类型；业务层的 `components/Toast.tsx` 直接使用 Base UI，并通过 effect 将 `message/onClose` 转换为通知管理器状态。不同组件对“公共封装”的理解不一致。

### 解决方案

**公共接口与依赖边界**

- 区分基础封装和应用组合。基础封装负责语义、焦点、样式槽与必要参数；`ModalShell` 等组合负责应用共用策略；业务组件只组合它们。
- 基础层的库兼容类型优先从已安装版本导出类型或组件 Props 派生；业务使用的稳定接口显式定义。删除手工复制的上游联合类型前，逐项核对取消关闭、事件传播和动画回调能力。
- 在 `eslint.config.mjs` 对 `ui/` 之外的前端源码限制 `@base-ui/react` 及其子路径的直接导入和转导出。先迁移现有例外，再启用规则，不留下无期限豁免。
- P1 先将 `Toast` 使用的底层 hook 移入 `ui/` 并通过应用封装导出，保持旧通知桥接行为，使导入规则能当批启用；Provider 和调用方式在 P4 统一迁移，避免批次相互阻塞。
- 保持现有 Fast Refresh 约束：新增 context、hook 和 provider 时拆分非组件导出，不为方便迁移全局放宽规则。

**按钮与表单基础视觉**

- 在现有 Button 上定义有限的 `variant`、`size`、`loading` 和图标位置接口，增加有可访问名称要求的 IconButton。具体变体以现有产品样式归类，不建立未使用的组合。
- 统一默认按钮类型、提交按钮显式声明、加载时禁止重复触发、图标装饰语义、焦点环和禁用反馈。
- 先迁移设置、主机表单和弹窗操作区，再迁移其他重复按钮。原生语义元素及终端专用控件按需保留，不做标签数量考核。
- 统一 Field、Input、Select、NumberField 的标签、帮助文字与错误关联，保留必要的输入组件适配。

**通知入口**

- 将底层 Toast 管理器使用限制在 `ui/`；应用根部只安装一次通知 Provider，通过应用级 `notify` hook 提交通知。
- 默认保留当前单条消息覆盖行为，明确超时、重复消息和关闭规则；多条堆叠属于后续产品决定。
- 将 `useSessions` 中的通用 toast 状态迁出，改为注入通知回调或由应用协调层处理；通知不再作为会话状态的一部分。
- 调用点迁移完成后删除旧 `message → effect → manager` 桥接。继续使用现有 Base UI，不再引入另一套通知库。

### 影响范围与验收

主要修改 `ui/button.tsx`、拟新增的 `ui/icon-button.tsx`、`ui/dialog.tsx`、`ui/toast.tsx`、通知公共模块、`components/Toast.tsx`、`hooks/useSessions.ts`、`App.tsx` 和 `eslint.config.mjs`。

- 类型检查覆盖上游回调签名；lint 验证业务层无法绕过公共入口。
- 用少量公共组件测试验证应用自己定义的默认类型、loading、可访问名称和通知覆盖策略；不重测底层库全部能力。
- 使用现有表单、连接、设置及权限流程测试验证行为；检查中英文、明暗主题、忙碌和禁用状态。

## 6. 问题四：表单与异步数据重复管理

本项拆成两个独立试点。它们分别解决字段状态和后端数据状态，不能通过一个全局状态容器混在一起。

### 6.1 表单方案

`HostForm.tsx` 有多组字段状态、条件校验、错误和提交状态；设置中的供应商配置也存在字段联动。先统一 Field 的错误展示，再选 `HostForm` 作为 React Hook Form 候选试点。

1. 定义表单草稿类型，与后端 `HostInput` 转换分开；端口字符串解析、范围、认证方式等规则集中为可测试的纯逻辑。保留后端权威校验和 IPC 错误码处理。
2. 试点统一字段注册、初值、错误、重置和提交状态。Base UI 受控组件通过明确适配接入，不让表单库类型扩散到所有公共 UI 组件。
3. 区分新建、编辑、切换目标和切换认证方式的行为；明确隐藏字段是否参与提交。密码清空和已有 keyring 凭据的保留语义按现有行为验证。
4. 密码和 API Key 不写入默认值缓存或持久化存储；完成、关闭或切换编辑目标时清理不再需要的敏感草稿。不要将其提交变量交给长期存活的通用 mutation 缓存。
5. 试点有效后推广到确有联动、动态字段和重复提交逻辑的设置表单。简单确认框、单字段密码框不要求使用表单库。
6. `ModelIdEditor` 保留为业务输入组件：分词、去重、数量限制、IME 和粘贴行为继续由其负责；只有实际需要候选项搜索时再评估 Autocomplete / Combobox，不能仅因外形相似直接替换。

当前 `tests/contracts/runtime-dependencies.test.ts` 禁止直接依赖 `zod`。本方案不引入 Zod，也不为试点放宽该契约；表单校验先使用明确规则和现有纯函数。

试点验收以 `host-form.test.tsx`、`host-picker-modal.test.tsx`、`settings.test.tsx` 和 `model-id-editor.test.tsx` 为基础，覆盖认证切换、端口错误、后端失败、重复提交、编辑重置、凭据保留及输入法组合输入。若封装复杂度抵消收益，则保留统一字段和纯校验改进，不推广新依赖。

### 6.2 异步数据方案

`useHosts` 手动维护读取和修改后的刷新；`SftpPanel` 有请求代次保护；`TunnelPanel` 自行管理读取、错误和选择同步。应先区分可缓存查询、用户命令与持续事件。

| 状态类别 | 处理方案 |
| --- | --- |
| 主机列表、非敏感设置读取 | TanStack Query 候选；先试点主机列表 |
| SFTP 目录与端口列表 | 后续按会话、路径和连接生命周期扩展 |
| 连接、命令、传输和凭据提交 | 显式业务命令；禁止自动重复执行 |
| 终端输出、Agent 流和传输进度事件 | 保留事件订阅与业务状态管理 |
| 弹窗、输入草稿、当前选中项 | 组件或功能内的 UI 状态 |

建议试点步骤：

1. 在应用组合层创建单个查询客户端，领域 hooks 仍封装 API 调用；组件不直接拼装查询配置。
2. 先迁移 `useHosts`，尽量保持对调用方稳定的接口；统一查询键和主机修改成功后的失效刷新。区分“操作失败”和“操作成功但刷新失败”，避免错误提示诱导重复创建或删除。
3. 初始配置关闭自动重试和窗口聚焦刷新，不持久化缓存；验证本地 IPC 查询不会因浏览器离线标记而被阻塞。需要后台刷新时按具体功能配置，不使用未经评估的全局默认值。
4. SFTP 查询键至少包含会话 ID 与规范化远端路径；会话代次会影响身份时也必须纳入。断连后停止新查询并清理相关缓存；旧会话结果不能覆盖新会话或新目录。
5. 保存、上传、删除成功后只刷新受影响目录；明确缓存有效期和释放条件。用户导航、选择、编辑器脏状态仍属于业务 UI。
6. 请求取消只代表前端不再接受结果，不能据此宣称 Go 后端操作已取消。保持现有 API 的取消能力与会话生命周期防护，测试后才移除重复保护。
7. 通用错误仍经过现有 IPC 错误解析；避免查询层再次产生重复 toast。读取失败由局部错误态展示，命令失败由流程指定反馈。

试点需补充异步顺序测试：延迟响应、连续刷新、切换路径、断连重连、卸载后返回、修改成功但刷新失败。扩展 `sftp.test.tsx`、`sftp-edit.test.tsx`、`tunnel-panel.test.tsx`，并补充主机列表流程测试。测试使用独立客户端与可控 Promise，不依赖公共网络。

### 新依赖的决策与退出条件

React Hook Form 和 TanStack Query 分开评估、分开提交。各试点记录：减少的重复状态与分支、增加的适配层、打包体积变化、交互测试结果和后续复用场景。实施时核对官方文档、项目 React 版本兼容性与锁文件，再选择版本；本文不预设版本升级。

只有试点能清楚简化状态归属、保留全部业务行为，且体积和启动开销可接受时才推广。若不满足，删除试点依赖和适配层，保留纯函数、字段关联、错误处理及竞态修复。两条路径都要完成本项维护性目标。

## 7. 问题五：入口、功能和样式边界过宽

### 7.1 拆分 App 的职责

- 提取设置读取、保存、主题和语言应用逻辑到设置功能模块。若后续采用查询缓存，它应成为已保存数据的唯一来源；编辑草稿单独管理，不再同步维护多份“当前设置”。
- 提取连接流程协调：密码请求、主机密钥确认、连接/重连、取消和凭据保存提示。使用显式状态或 reducer 表达合法转移，底层会话与终端数据订阅仍由会话模块负责。
- 提取应用弹窗组合与权限请求队列，明确请求如何完成、取消及卸载收尾。并发确认请求不能互相覆盖，等待中的 Promise 必须有明确的解决策略。
- 让 `App.tsx` 保留 providers、主布局和模块接线。先抽职责，再随功能移动目录；避免只把大组件拆成多个仍共享全部状态的渲染函数。

### 7.2 按功能迁移

优先迁移 hosts 和 settings，再处理 monitoring、transfers、agent、sftp，最后迁移 sessions 的终端适配。高风险文件在行为稳定后再搬迁。

每个功能拥有业务组件、hooks、纯逻辑和局部样式；通用 `ModalShell`、通知和基础控件留在 `ui`，共享产品布局由应用层拥有。现有 `components/`、`hooks/` 随迁移缩小，不保留长期的新旧双入口。

测试继续放在 `tests/`，通过功能入口或实际用户行为测试；复用 `tests/support/helpers.tsx` 的 fake API 和事件工具，不跟随源码搬到仓库各处。

### 7.3 拆分样式

1. 先识别主题变量、应用布局、公共控件和业务样式的归属，记录当前级联和覆盖关系。
2. 公共控件样式归入 `ui`；新迁移功能优先使用 CSS Modules。`theme.css` 保留全局主题变量，`App.css` 最终只承担应用框架布局及少量全局基础规则。
3. 每批按一个控件或功能迁移，保留原样式值，迁移后删除旧规则。验证 import 顺序与选择器优先级，避免同时保留两套覆盖来源。
4. 将通用颜色、间距、圆角、焦点环和弹层层级逐步抽为已有主题体系中的变量，不一次性重写全部视觉规范。
5. Portal 弹层使用明确样式类或槽参数，避免依赖已不存在的父级 DOM 选择器；同步检查 Base UI 状态属性、退出动画、hidden / inert 和 reduced-motion。

验证主机表单、设置、终端、SFTP、Agent、传输中心和端口面板的明暗主题、中英文长文本、窗口缩放和错误状态。不得因局部 CSS 迁移导致其他功能变化。行数下降仅作观察指标，不设置文件行数硬门槛。

## 8. 实施批次与依赖

下表中的批次可分别提交评审；存在依赖时应顺序实施，避免同一批同时改变交互、目录和样式。

| 批次 | 交付内容 | 依赖 | 完成条件 |
| --- | --- | --- | --- |
| P0 | 基线测试、关键页面截图、已有失败与体积记录 | 无 | 建立可对比基线，标明已存在的问题 |
| P1 | Button / IconButton 接口、类型派生、底层导入边界 | P0 | 公共接口可用，现有导入迁移后 lint 通过 |
| P2 | 中性 Tabs 接口、侧边栏迁移、会话标签迁移 | P1 | 键盘交互与终端保活检查通过 |
| P3 | 菜单受控状态与监听收敛、弹窗关闭策略整理 | P1 | 焦点、Escape、关闭时机及真实 WebView 验收通过 |
| P4 | 单个通知 Provider 和应用通知入口 | P1 | 删除双状态桥接，连接与设置通知行为保持一致 |
| P5 | 字段关联和纯校验整理、HostForm 表单试点 | P1、P4 | 记录依赖采用或退出决定，表单契约全部通过 |
| P6 | 主机查询试点，再评估设置/SFTP/端口列表 | P4 | 查询所有权清晰，竞态与失败场景通过，记录采用决定 |
| P7 | App 职责抽离、功能目录逐批迁移、功能样式隔离 | P2—P6 的相关部分完成 | 每个功能只保留一个入口，删除已迁移旧样式和兼容层 |
| P8 | 全量回归、平台验收、文档和遗留项收尾 | P7 | 构建与平台门禁通过，未完成事项有明确归属 |

P5 和 P6 是独立决策，不捆绑引入依赖。P7 中不涉及试点状态的职责抽离可提前进行，但同一功能的状态迁移完成后再移动文件。具体工期在 P0 基线与 P2 终端试改后估算，不在证据不足时承诺日期。

### P0 自动检查基线

2026-11-12 已在独立临时目录从 `git archive HEAD` 提取原版本，复用本次 `npm ci` 安装的锁文件依赖进行对照。临时目录中的原配置和测试路径保持 HEAD 状态，工作区保留此前的目录整理与 P1 改动。

- 本机环境：macOS ARM64、Node 26.13.0、npm 11.20.0、Go 1.27.4。与 CI 的 Node 22 / Go 1.26 环境不同，最终兼容性仍由 CI 检查。
- 原版本 `npm run typecheck`、`npm test`、`npm run build:wails` 均通过；25 个测试文件、300 项测试。
- 原版本 ESLint 为 0 errors / 217 warnings；构建已有大于 500 kB 的 chunk 提示；jsdom 测试已有 Canvas `getContext()` 未实现提示。
- 原版本 Vite 输出：JS 2,424.41 kB（gzip 768.63 kB），CSS 83.63 kB（gzip 14.69 kB）。体积对照使用同一工具链与依赖，不等同于真实启动或交互性能测量。
- 原生 WebView 截图、跨平台交互和性能场景尚未采集，P0 仅完成自动检查与构建体积基线。

### P1 实施记录

- `ui/Button` 统一默认 `type="button"`、primary / secondary / danger 变体、小尺寸、图标槽和 loading 禁用语义；`IconButton` 要求显式可访问名称。已迁移主机表单、确认动作、主机选择操作及设置关闭按钮。
- `ui/dialog.tsx` 和 `ui/alert-dialog.tsx` 的各部件 Props、关闭原因、焦点目标和状态类型从已安装的 Base UI 组件类型派生。公共动画回调仅接收 `open` / `transitionStatus`，避免将 Popup 专有状态错误要求于 Backdrop；保留 `ModalShell` 的应用关闭策略。
- `components/Toast.tsx` 改从 `ui/use-toast-manager.ts` 读取管理器 hook，继续沿用单条消息覆盖和现有 `message` / `onClose` 接口；Provider 与应用通知入口留在 P4。
- ESLint 限制 `ui/` 之外直接导入或转导出 `@base-ui/react`；新增 TypeScript AST 合约测试覆盖静态、动态导入及转导出。
- 新增 Button / IconButton 接口测试，覆盖默认非提交行为、显式提交、变体、loading 阻止点击、函数型样式类合并和图标动作名称。
- 按用户指定，由 GPT 6 Luna（max）子代理参与实施，主代理准备依赖、复核改动并执行全量门禁。此前 Terra 调用因模型通道 HTTP 404 未执行改造。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 均已通过；27 个测试文件、305 项测试。Base UI 1.9.0 的类型兼容已由实际编译确认。另以 ESLint API 验证业务路径的直接导入和转导出均被规则拦截。
- ESLint 为 0 errors / 214 warnings（基线 217）；未对无关文件进行全仓格式化。构建和 jsdom 原有提示仍存在。
- 本轮未新增依赖或修改锁文件。Vite 输出 JS 2,423.54 kB（gzip 768.64 kB），CSS 83.77 kB（gzip 14.73 kB）；基线 JS 2,424.41 kB、CSS 83.63 kB。
- 本阶段未执行原生 Wails WebView 视觉与交互验收、Go 检查或跨平台构建；P0 截图和性能采样仍待补齐。原有 Wails/IPC/API 契约未改动。P1 交付后的下一阶段为 P2 标签迁移。

### P2 实施记录

- 将 `FormTabs*` 改为中性命名 `TabsRoot` / `TabsList` / `TabsTab` / `TabsPanel`，同步迁移 MCP 设置，删除旧导出；继续从已安装的 Base UI Props 派生类型，无新增依赖或锁文件变更。
- 侧栏使用受控 Tabs，显式采用手动激活：方向键、Home / End 移动焦点，Enter / Space 或点击选择面板。Monitor / Ports 均使用 `keepMounted`，隐藏面板仍接收事件，`hidden` / `inert` 与标签面板的 ARIA 关联由基础组件维护。
- 会话使用受控 Tabs 和稳定会话 ID，方向键聚焦时激活对应会话。主机入口、新建会话、传输中心和 Agent 位于 `tablist` 之外；标签触发器与关闭按钮为同级独立控件，无嵌套按钮。
- 每个会话的 `TabsPanel` 持续挂载，终端实例随面板保持。连接占位会话不创建 xterm，重连等待和失败保留旧实例，成功更换会话 ID 后只替换目标终端。SFTP 仍使用原有活动会话与连接状态契约，`useSessions` 和 `TerminalView` 的业务实现未修改。
- 关闭活动会话继续由 `useSessions` 选择最后一个剩余会话，关闭非活动会话保持选中项。移除持有焦点的关闭控件后恢复到选中标签，关闭最后一项后恢复到主机入口；连接占位标签替换时也交接标签焦点。等待 IPC 期间用户进入弹窗或其他控件时不抢焦点。
- 最小范围调整标签布局、水平滚动、按钮样式重置、焦点环和面板隐藏规则；新增中英文会话列表可访问名称，保留原有外观体系。
- `session-terminal-persistence.test.tsx` 由 7 项扩展为 15 项，覆盖方向键与首尾导航、ARIA 关联、后台输出、重复切换、活动首项 / 中间项 / 末项关闭、非活动项及最后一项关闭、异步焦点、连接占位关闭与替换、重连失败保活及成功替换。
- 新增 `sidebar-tabs.test.tsx` 2 项业务测试，验证手动激活、ARIA、隐藏与 inert、后台监控快照更新，以及只在卸载时退订。设置、连接、端口、SFTP 等现有测试继续通过。
- GPT 6 Luna（max）子代理负责公共接口、MCP 设置及侧栏迁移与侧栏测试；主代理实现会话迁移、补充终端回归、复核并执行完整门禁。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 和 `git diff --check` 均通过。28 个测试文件、315 项测试（P1 为 27 个文件、305 项）；ESLint 为 0 errors / 208 warnings（P1 为 214）。原有 jsdom Canvas 和大 chunk 提示仍存在。
- Vite 输出 JS 2,424.32 kB（gzip 768.76 kB），CSS 84.46 kB（gzip 14.84 kB）；P1 为 JS 2,423.54 kB、CSS 83.77 kB。未做真实启动或持续输出性能测量。
- 本阶段未执行原生 Wails WebView 视觉 / 焦点验收、Go 检查或跨平台构建。待验收包括明暗主题、中英文、窄窗口标签滚动、真实终端输入焦点和连接 / 关闭流程；jsdom 通过不代表这些原生场景已验证。后续阶段为 P3，本轮未开始。

### P3 实施记录

- `TerminalView` 和 `SftpPanel` 各自仅保留一份可空菜单位置与业务目标；两个业务菜单改为受控 `open`，删除内部显示状态。公共 Root 持续挂载，Portal 与关闭生命周期由 Base UI 管理。
- 删除宿主的全局 `mousedown` / `keydown` 关闭监听及生产代码对 `data-testid` 的行为判断。公共菜单改用 Base UI 的 `Menu.Root modal={false}` 与虚拟坐标锚点：当前打开方式由宿主发起，不依赖 `ContextMenu.Trigger`，因而不再受到该 Trigger 配套的手势保护期和模态遮罩影响。定位、方向键、Escape、外部点击和动作关闭仍交给基础库。
- 公共封装透传基础库的关闭详情与焦点目标类型；外部点击及已经聚焦的新控件优先，避免关闭菜单时抢回焦点。Escape / 普通菜单动作关闭后，终端回到当前 xterm textarea，SFTP 回到原文件行；编辑 / 删除弹窗打开时让弹窗接管焦点。
- 终端通过 xterm 的 `attachCustomKeyEventHandler` 在菜单显示期间隔离远端输入；菜单关闭后的 Escape 和文字继续发送到 SSH。终端隐藏、SFTP 会话切换 / 收起 / 断连时清理菜单目标，卸载时移除 Portal。
- 核对已安装 Base UI 的退出完成机制，保留 `ModalShell` 的既有参数和生命周期，补充关闭职责注释；`useModalClose` 仅请求退出，拥有者回调在动画完成后执行。不额外建立重复的关闭状态，保留密码外点限制、忙碌关闭限制、嵌套 Escape 传播与 reduced-motion。
- 扩展菜单与宿主回归，覆盖受控关闭请求、禁用项、键盘选择、外部点击、连续右键更换选择 / 文件目标、终端按键隔离与焦点恢复、SFTP 弹窗焦点交接及生命周期清理。弹窗测试用可控退出动画验证重复关闭请求只通知一次，并补充嵌套 Escape 传播检查。
- GPT 6 Luna（max）子代理负责弹窗职责核对及回归测试，并只读复核 SFTP 菜单；主代理实现菜单迁移、终端键盘桥、菜单与焦点测试，完成集成检查。未新增依赖、修改锁文件或推进 P4。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 和 `git diff --check` 均通过。28 个测试文件、325 项测试（P2 为 315）；ESLint 0 errors / 208 warnings，与 P2 相同。初次与构建并行运行时 7 项测试超时或连带失败，构建结束后单独重跑原 `npm test`，全部通过，未放宽超时或修改测试并发配置。
- Vite 输出 JS 2,423.84 kB（gzip 768.70 kB），CSS 84.46 kB（gzip 14.84 kB）；P2 为 JS 2,424.32 kB、CSS 84.46 kB。原有 jsdom Canvas 与大 chunk 提示仍存在。
- 前端构建后 `go vet ./...` 通过；过滤 `node_modules` 后逐包运行 `go test -timeout 90s`，21 个含测试的包全部通过，`internal/sshtest` 为无独立测试的辅助包。未启用 race，未修改 Go 源码。
- 本阶段代码与前端自动检查已完成，真实 Wails WebView 验收尚未执行，不能标记 P3 的完整验收条件已满足。待验收：明暗主题、中英文、窗口边缘重定位、连续右键、真实终端按键 / IME、剪贴板和菜单到弹窗的原生焦点交接；跨平台构建与 smoke 仍以 CI / P8 为准。

### P4 实施记录

- 在 `App` 根部安装唯一的 `NotificationProvider`；业务通过 `ui/use-notify.ts` 的 `useNotify()` 直接提交消息。Provider 组合已有 Toast primitives，并呈现唯一 viewport；Base UI 管理器是通知列表与计时器的唯一来源。
- 删除 `components/Toast.tsx` 的旧 `message → effect → manager` 桥接、`App` 的派生 `toastMessage` 和 `useSessions` 的 `toast` / `setToast`。`useSessions` 通过注入回调报告错误，订阅不因通知或语言重渲染而重建；会话错误状态、连接重试和终端保活保持原流程。
- 主机读取错误此前只作为旧 Toast 的消息源，现同样通过 `useHosts` 的注入回调上报，移除对应展示状态。保留原有读取与刷新策略，本轮未引入查询缓存或推进 P6。关闭通知后，普通重渲染不会重新显示同一个历史错误。
- 默认仍为单条覆盖与 `timeout: 0` 手动关闭；重复消息更新同一条通知，并刷新显式超时；关闭后的同文消息可以重新发送。省略 timeout 的后续消息会清除上一条的自动关闭计时器，不继承旧 tone。正文按发送时的本地化结果展示，关闭按钮随界面语言更新。
- 沿用 Base UI 固定 ID upsert、退出完成和计时器清理机制，不新增 React 消息状态、应用定时器或全局通知单例。Provider 卸载后，保留的旧异步回调不能再次发布消息或建立旧计时器；新挂载的应用不接收旧通知。
- 连接和凭据失败在调用处继续解析 IPC 错误并选择提示文字，通知正文不暴露 `NODESHELL_ERR` 协议前缀；密码认证错误继续留在密码弹窗。设置保存失败仍按原规则回滚，并进入同一通知入口。
- 增加通知内部模块的 ESLint 导入限制，并扩展 AST 合约测试覆盖业务静态 / 动态导入与转导出。业务使用 `useNotify`，根组合使用 `NotificationProvider`，不能直接读取 manager、Toast primitives 或通知 Context。
- 新增 `notifications.test.tsx` 7 项真实组件测试，覆盖多调用方单条替换、重复和重发、可控退出动画完成后的新消息存活、默认手动关闭、显式超时刷新、中英文关闭名称、StrictMode、卸载及迟到回调。新增 5 项应用流程测试，覆盖连接错误本地化、凭据错误解析、设置 / 会话消息覆盖、通用重连失败和主机读取失败；增加密码错误不产生双重通知的断言。
- 本阶段代码和测试由主代理完成；GPT 6 Luna（max）子代理只读复核通知生命周期、固定 ID upsert 及 hooks / App 迁移，未发现实际缺陷。未新增依赖或修改锁文件，本轮未进入 P5。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 通过；29 个测试文件、338 项测试（P3 为 28 个文件、325 项），ESLint 0 errors / 208 warnings。新增的计时器测试使用 fake clock；自动点击工具与 fake clock 的等待冲突已通过直接派发按钮点击解决，没有放宽超时或修改公共测试配置。
- Vite 输出 JS 2,423.92 kB（gzip 768.76 kB），CSS 84.46 kB（gzip 14.84 kB）；P3 为 JS 2,423.84 kB、CSS 84.46 kB。原有 Canvas `getContext()` 与大 chunk 提示仍存在。
- 构建后 `go vet ./...` 通过；过滤 `node_modules` 后逐包执行 `go test -timeout 90s`，21 个含测试的包全部通过（未变动的包复用 Go 测试缓存），`internal/sshtest` 无独立测试；未启用 race。
- 本阶段未执行原生 Wails WebView 的明暗主题通知外观、长文本、真实焦点和桌面交互验收，也未执行跨平台构建或 smoke。代码与自动门禁完成，原生检查继续作为待验收项保留。

### P5 实施记录

- `FormField` 增加可选 description / error 槽，使用 Base UI 的 Description / Error 自动关联控件与帮助、错误；只在外部存在错误时声明 invalid，保留原调用方的内建验证。`PasswordField` 复用同一 Field / Input，保留显隐、自动完成和密码弹窗的显式 ARIA 参数，并增加 inputRef / onBlur 适配能力。公共 UI 不引用表单库类型。
- 将 HostForm 草稿、初值、十进制整数端口解析、条件校验和 HostInput 转换提取至 `components/host-form-model.ts`。补齐中英文必填错误，拒绝空白、非数字、小数、指数和越界端口；业务 DTO 与短期密码仍分开，后端验证、keyring 和本地路径防护未变。
- 保留编辑时空密码不更新凭据、已有私钥路径清空时省略路径的既有语义；认证字段按当前方式转换，隐藏密码 / 私钥路径不进入载荷。切换认证时清空密码；成功后清空密码，失败时保留当前草稿以便重试，关闭、编辑身份切换及卸载时释放敏感草稿。主机初值永远不包含实际密码，不将凭据提交给全局缓存、持久化或日志。
- 采用 React Hook Form **7.92.0**，精确锁定并只新增此一个包。实施时核对官方 npm 元数据：React peer 包括 19，Node 要求 >=18；核对官方 useForm 默认值缓存 / reset / 注销文档及 Base UI 的外部表单库接入指南。来源：`https://react-hook-form.com/docs/useform`、`https://base-ui.com/react/handbook/forms`。保持 Zod 禁用契约，未引入 resolver 或查询库。
- HostForm 删除七份字段 useState、单独表单错误和 submitting 状态；使用注册字段、认证 / 密码两个 Controller、单个纯校验 resolver 和 formState。RHF 类型仅留在业务表单；没有建立通用表单工厂。Base UI Select 保持原稳定接口；本轮未把它扩展为全库 Props 代理。
- 编辑身份使用独立 key 命名空间，新建与任意导入的主机 ID 不会冲突；普通父组件重渲染和同 ID 输入保留草稿，切换 ID / 编辑与新建时重建初值。同步提交门闩在异步校验前阻止重复事件；卸载后校验不会再发起保存，旧保存 / 文件选择结果不能污染新表单。文件选择只接受最新请求，并在认证切换、提交、卸载时使旧结果失效。
- 主机管理器使用表单提交状态控制忙碌策略：保存期间禁用认证和字段、取消、关闭、新建、编辑、连接、删除与目标切换，同时限制 Escape / 外部关闭。保存完成仅清理原来的 formMode；未把前端关闭宣称为取消后端操作。失败继续内联显示解析后的 IPC 消息并允许重试。
- 采用决定：保留当前 HostForm 试点，后续只在确有联动和提交生命周期的复杂表单评估复用；暂不推广设置、简单确认框或密码框。收益是字段状态、错误与提交生命周期归于一个表单对象，首错聚焦和条件字段管理由库承担；成本是两个 Controller、resolver、提交门闩及与父弹窗共享忙碌状态。异步文件选择与 IPC 生命周期防护仍属于业务，不为减少行数删除。HostForm JSX 与保护代码有所增加，不能以行数宣称收益。
- 新增纯模型测试及公共 Field 关联测试；扩展 HostForm 与 HostPickerModal 测试，覆盖校验 / 聚焦、认证切换、密码清理、编辑重置、普通重渲染、重复提交、失败重试、凭据保留、IPC 错误解析、语言切换、IME 合成事件、迟到响应、保存期 Escape / 外点 / 目标切换，以及导入 ID 与新建 key 冲突。ModelIdEditor 保持分词、去重和 IME 责任，未替换为通用候选输入。
- 主代理实施和执行验证，GPT 6 Luna（max）只读复核，发现并修复导入 ID 为 `create` 的身份冲突。未修改 Go/API/CI，也未进入 P6。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 和 `git diff --check` 通过；31 个测试文件、372 项测试（P4 为 29 个文件、338 项），增加 34 项测试。ESLint 0 errors / 200 warnings（P4 为 208，修改 HostPickerModal 时按项目 Prettier 规则整理了该文件）。原有 Canvas `getContext()` 和大 chunk 提示仍存在。
- Vite 输出 JS 2,470.23 kB（gzip 784.80 kB），CSS 84.53 kB（gzip 14.85 kB）；对比 P4 的 JS 2,423.92 kB（gzip 768.76 kB）和 CSS 84.46 kB（gzip 14.84 kB），JS 增加 46.31 kB / gzip 16.04 kB，CSS 增加 0.07 kB。此次受限试点接受该构建增量，未来推广仍需评估启动与交互成本。
- 前端构建后 `go vet ./...` 通过；过滤 `node_modules` 后逐包执行 `go test -timeout 90s`，21 个含测试的包通过，`internal/sshtest` 无独立测试。最终身份 key 修复及重新构建后再次执行 vet 和根包测试；未变化的内部包复用缓存，不启用 race。
- 真实原生 Wails 的字段错误布局、明暗主题、窄窗、系统文件对话框焦点、自动完成和中文输入法仍待验收；jsdom 的 composition 测试不代替原生 IME。未执行跨平台打包或 MCP smoke，未测量启动时间或运行时性能，尚不能证明启动开销无变化。

### P6 实施记录

- 采用限定于主机列表的 TanStack Query **5.107.1** 试点，精确锁定 `@tanstack/react-query`，仅新增它和同版本 `query-core` 两个包。官方 npm peer 元数据支持 React 18 / 19，与项目 React 19 相容；核对官方查询取消与网络模式说明：`https://tanstack.com/query/latest/docs/framework/react/guides/query-cancellation`、`https://tanstack.com/query/latest/docs/framework/react/guides/network-mode`。
- `app/query-client.ts` 集中默认策略，`AppQueryProvider` 每次应用挂载创建唯一内存客户端，不持久化或共享全局单例。关闭自动重试、聚焦和联网恢复刷新，设置 `networkMode: 'always'`，本地 IPC 不受浏览器离线标记阻塞。应用卸载清空缓存，延迟清理避开 StrictMode 的 effect 重放；未使用的查询五分钟后回收。
- `hooks/hosts-query.ts` 定义唯一主机查询键与读取函数；`useHosts` 将列表、加载、刷新、错误及是否已有数据交给 Query 管理，删除原来的列表读取 effect 和独立 React 读取状态。主机列表按显式操作更新，打开管理器、CRUD 和凭据流程主动刷新，不增加轮询或后台监听。
- 刷新先取消旧查询，再失效并读取；写成功后先取消旧读取，把后端确认的结果合入列表，再执行权威刷新。成功写入不会因后续读取失败而被当作失败，也不会诱导再次创建或删除；迟到旧列表不能复活已删除主机或覆盖凭据标记。hook 生命周期代次阻止卸载后的写结果更新旧客户端。读取使用 AbortSignal 丢弃取消后的结果，底层 Wails IPC 仍会执行，未宣称后端操作可取消。
- 主机管理器新增首次加载、首次读取错误、已有数据的刷新错误、手动刷新和局部重试；首次失败不显示虚假的空列表，刷新失败保留最后确认的数据，并提示成功操作无需重做。主机读取错误不再走 P4 的通知回调，避免 toast 与局部错误重复。删除命令失败内联显示解析后的消息并保留编辑草稿，删除期间阻止重复操作、关闭和目标切换。
- 连接成功后，凭据提示必须等待新的主机元数据读取，不能仅依赖无限 staleTime 的缓存；读取失败或主机已移除时跳过提示，读取错误留在主机管理器。跳过保存的 `markPrompted` 失败在凭据流程内捕获，不误报 SSH 连接失败；保存 / 标记失败释放本次提示记录，允许后续连接重新询问。
- create / update / remove / credentials 继续显式调用 API，不使用 `useMutation` 或通用 mutation variables。缓存仅接收非敏感 HostConfig；密码仍只存在短期表单 / 业务调用并交给系统 keyring，不放入主机 DTO、查询缓存、持久化或日志。未改 Go / IPC API / CI，未进入 P7。
- 采用决定：保留主机列表试点，收益是读取状态归于一个所有者、多个消费者共享读取、失败与已确认写入区分明确；成本是客户端 / Provider / 查询配置和业务 reconcile / 生命周期防护。生产代码总量有所增加，不能以行数减少作为收益。当前接受受限试点的构建增量，启动与交互成本仍需实测，暂不扩大使用范围。
- 后续范围评估：设置有即时乐观回滚、字体防抖写入和多种偏好状态，需先定义读取快照与草稿 / 写入所有权；SFTP 的 chdir / cwd 是后端有状态操作，目录查询必须先定义会话与路径身份、断连清理、传输刷新和脏编辑器行为；Tunnel 的 discover / list 与 start / stop、选择项共享会话生命周期，需先定义会话键与旧操作结果隔离。本轮均保留原实现，不删除已有保护。SFTP 在断连或折叠时切换会话、Tunnel 在切换会话后旧请求返回，仍存在待单列验证与修复的迟到结果风险，不能把主机试点的竞态保证外推到它们。
- 新增 `hosts-query.test.tsx` 9 项测试，扩展管理器 3 项、应用流程 5 项；覆盖共享查询、离线、无自动刷新、连续延迟读取、删除后旧列表、凭据刷新、写成功读失败、命令拒绝、卸载和 StrictMode；界面回归包括局部错误重试、删除失败保留草稿、凭据标记失败不破坏连接。主代理实施与验证，GPT 6 Luna（max）只读复核并指出凭据标记拒绝的错误归属问题，已修复并增加回归。
- `npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 和 `git diff --check` 通过；32 个测试文件、389 项测试（P5 为 31 个文件、372 项），增加 17 项。ESLint 0 errors / 200 warnings，与 P5 数量相同；原有 Canvas `getContext()` 和大 chunk 提示仍存在。
- Vite 输出 JS 2,506.82 kB（gzip 795.61 kB），CSS 84.66 kB（gzip 14.88 kB）；对比 P5 的 JS 2,470.23 kB（gzip 784.80 kB）和 CSS 84.53 kB（gzip 14.85 kB），JS 增加 36.59 kB / gzip 10.81 kB，CSS 增加 0.13 kB / gzip 0.03 kB。
- 构建后 `go vet ./...` 通过；过滤 `node_modules` 后逐包执行 `go test -timeout 90s`，21 个含测试的包通过，`internal/sshtest` 无独立测试。最终凭据流程修复与重新构建后再次执行 vet 和根包测试；未变化的内部包复用缓存，未启用 race。
- 原生 Wails WebView 的加载 / 错误布局、明暗主题、窄窗、焦点及离线标记行为仍待人工验收；未执行跨平台打包或 MCP smoke，也未测量启动时间和运行时性能。自动测试不能替代这些门禁。

### P7 实施记录

- 完成 App 职责抽离：`settings/useAppSettings` 管理非敏感偏好；`sessions/useConnectionFlow` 用 reducer 协调密码、连接/重连、主机密钥与凭据提示；`hosts/useHostCommands` 管理创建后连接和编辑后的凭据命令；`app/AppOverlays` 组合弹层。`App.tsx` 从 P6 的 868 行收敛为 203 行，保留 providers、主布局和模块接线。
- 应用内确认改为 FIFO 队列，按请求身份完成，忽略旧/重复回调，卸载时所有等待 Promise 返回 false。后端权限队列独立去重并防止重复提交；提交失败保留请求供重试，关闭事件移除对应请求；卸载退订，后端会话与超时策略仍负责权限请求终结。
- 设置保留乐观更新和字体大小 300ms 防抖，同字段写入串行发送；旧成功不覆盖新意图，旧失败不回退新值，最新失败回到已确认基线。晚到初始化结果不覆盖已编辑值或已保存基线；StrictMode 和卸载后结果均有生命周期保护。没有将设置扩展进查询缓存。
- 连接与主机命令在异步边界检查生命周期，卸载后的结果不继续发起主机密钥重试、刷新、提示或凭据写入；同步门闩阻止重复连接。密码只在短期草稿和直接命令中传递，不进入 reducer、HostInput 或查询缓存。保留 P5 草稿与 P6 主机读取/写入错误语义。
- 全部业务组件、hooks 和纯逻辑迁入 hosts、settings、monitoring、tunnels、transfers、agent、sftp、sessions、permissions 九个功能。每个功能只有一个公开 `index.ts`，跨功能与应用导入通过该入口；公共 ModalShell、ConfirmModal、PasswordField、ErrorBoundary 归 `ui/`。旧 components/hooks 目录及 Select 转导出已移除，源码、测试和 mock 导入同步更新。
- 保留会话稳定 ID、Tabs `keepMounted`、终端订阅与数据路由。MonitorPanel 从侧栏组合中抽离，PasswordModal 从 App 中抽离。SFTP 和 Tunnel 只迁移目录/样式，P6 记录的迟到结果风险继续暂缓，不宣称获得主机查询的竞态保证。
- `App.css` 从 P6 的 4804 行收敛为 256 行；公共 reset、弹层、表单、菜单和状态样式归 `ui/surfaces.css`，九个功能分别使用 CSS Modules。保留共享 Portal、状态和 CodeMirror 全局类契约，供主机/SFTP 共用的历史设置弹层头部基础规则仍在 `ui/`。一次性 PostCSS 对比确认 726 组选择器/声明保留；行数下降不代表代码总量或运行性能改善。
- Vitest 使用非作用域 CSS Module 类名保持行为测试选择器稳定，生产 Vite 仍生成作用域类名；该设置和样式声明比对不能替代真实级联与视觉验收。新增架构合约限制跨功能深层导入、features 反向依赖 app 及 ui 依赖业务层。
- 新增设置、确认队列、权限队列和连接生命周期四组测试，加上架构合约共增加 16 项。`npm run typecheck`、`npm test`、`npm run lint`、`npm run build:wails` 通过：36 个测试文件、405 项测试；ESLint 0 errors / 54 warnings（P6 为 200，剩余 52 项格式警告与 2 项既有 Agent hook 警告）。原有 jsdom Canvas 和大 chunk 提示仍存在。
- Vite 输出 JS 2,527.95 kB（gzip 801.21 kB），CSS 91.09 kB（gzip 16.38 kB）；对比 P6，JS 增加 21.13 kB / gzip 5.60 kB，CSS 增加 6.43 kB / gzip 1.50 kB。未新增依赖、修改 Go/API 或 CI；构建体积不等同于运行性能。
- 本机默认 Go 1.27.4 下 `go vet ./...` 与逐包测试通过，但 Wails 构建的包分析报 `package "errors" without types`。临时指定项目要求的 `GOTOOLCHAIN=go1.26.0` 后，`wails build -skipbindings -s -m -nosyncgomod -nocolour` 完成 macOS ARM64 编译、打包与自签名；使用已构建前端，未修改项目依赖或全局 Go 配置。Go 1.26 下再次通过 vet 和全部 22 包检查：21 个含测试包通过，sshtest 无独立测试；逐包 90 秒超时，未启用 race。
- 使用该新原生程序运行 `scripts/mcp-smoke.sh` 通过：initialize、10 个工具列表与 ping 共 3 个响应，stderr 为空。原生窗口未启动进行交互验收；尝试浏览器夹具时，浏览器服务返回 `The browser access host is unavailable`，夹具已停止，未把此尝试计为视觉通过。
- 新增当前结构文档并更新 AGENTS、文档索引和测试指南。本阶段由主代理实施，未调用子代理。下一阶段为 P8；明暗主题、窄窗、焦点、IME、拖放、跨平台打包及性能采样仍待验收，不能据自动检查标记整份计划完成。

### P8 本机实施与验收记录

- 修复 P6/P7 记录的 SFTP 与 Tunnel 迟到结果风险。SFTP 会话/连接身份变化后清空旧状态与弹层，目录操作串行且绑定明确绝对路径，较晚导航失败后读取权威 cwd/list；Tunnel 读取版本与命令生命周期分离，阻止重复命令和切换后剩余批量项发送，旧 finally 不解除新命令忙碌状态。已发送 IPC 不能由这些前端保护取消，也不自动逆转后端命令。
- 新增 SFTP 8 项、Tunnel 6 项延迟响应回归及四会话/800 段混合输出回归；总数从 36 文件/405 项增加至 38 文件/420 项。修正测试 i18n wrapper，使 rerender 保持 Provider 与表单草稿身份，语言切换继续用同一实例。
- 修复 Agent effect 的不稳定空列表与模型依赖，清理剩余格式警告。TypeScript token 对比确认 adapter 和共享 DTO 仅格式化。已核对默认 Wails macOS 模板并忽略其生成路径，构建输出不进入版本控制。
- 使用与 CI 同主版本的临时 Node 22.23.5 通过完整 typecheck/test/lint/build；ESLint 0 errors / 0 warnings。最终前端 JS 2,530.05 kB（gzip 809.75 kB），CSS 91.09 kB（gzip 16.32 kB）。在相同 Node 22 与已安装依赖下重建 HEAD 基线，JS 2,424.41 kB（gzip 776.24 kB）、CSS 83.63 kB（gzip 14.66 kB）；完整体积与边界见验收报告，不与此前 Node 26 的 gzip 值混比。
- Go 1.26 下 vet 通过；22 包逐包 `-count=1 -timeout 90s`，21 个含测试包通过，sshtest 无独立测试；最终前端重新构建后再检查 vet 和根包。macOS ARM64 Wails 编译/打包/自签名与最终程序 MCP smoke 通过，使用现有脚本生成本地 DMG/ZIP。
- 本轮未新增依赖、改动 Go/API/CI、调用子代理、提交或发布。保留原有 Canvas 和大 chunk 提示。四会话负载是正确性回归，未测真实 xterm/WebView 性能；原生输入、窗口布局、其他平台、性能和正式签名发布未标为通过。
- 浏览器服务返回不可用，Computer Use 返回未获准访问 NodeShell，故本轮不能完成原生交互验收，未绕过限制。[验收报告](../reports/ui/frontend-p8-acceptance.md)列出每项状态与后续执行位置；当前是本机自动门禁完成，P8 发布验收仍有待完成项。

## 9. 验证与发布门槛

### 自动检查

每批运行针对修改的测试和类型检查，提交前执行前端完整门禁：

```bash
npm run typecheck
npm test
npm run lint
npm run build:wails
```

单项测试通过仓库脚本加载移动后的配置，例如：

```bash
npm test -- tests/ui/session-terminal-persistence.test.tsx
npm test -- tests/ui/terminal-view-contextmenu.test.tsx
npm test -- tests/ui/host-form.test.tsx
npm test -- tests/contracts
```

最终按 CI 顺序在前端构建后运行 `go vet ./...` 和 Go 测试，过滤 `node_modules` 包，逐包使用 90 秒超时；不擅自开启已有挂起历史的 race 模式。原生 Wails 构建、MCP smoke 和打包继续以现有 CI 为准。

新增测试关注应用自己定义的行为，不对基础库内部实现建立快照。纯校验和数据转换放 `tests/unit/`，交互与流程放 `tests/ui/`，必要的架构和依赖约束放 `tests/contracts/`，公共辅助放 `tests/support/`。

### 真实应用与性能检查

- 在原生 Wails 应用检查焦点、菜单、剪贴板、IME、拖放、窗口缩放和系统主题切换。聚焦交互高风险变更先完成本机验收，最终按 Windows、macOS、Linux 原生环境覆盖。
- 基线和最终版本用同一套场景对比：多会话持续输出并切换、SFTP 大目录与快速导航、Agent 长对话、多个传输任务；记录场景规模、采样方法和实际结果。
- 检查终端重复挂载、事件订阅泄漏、关闭后的资源释放、缓存增长和渲染次数；新增依赖记录构建产物变化。
- 如果 SFTP 大目录实测存在渲染瓶颈，再单列虚拟化方案。表格库、图表库和组件展示平台也按实际需求评估，不纳入本轮必选依赖。

规划时缺少 Node/npm/Go；实施时已准备本机工具链与依赖。已执行项目和结果记录在上面的 P0—P8 实施记录及 P8 验收报告中，未执行的原生交互、性能与跨平台门禁继续保留。

## 10. 风险、回退与完成定义

| 风险 | 防护与回退方式 |
| --- | --- |
| 标签迁移导致终端重建或丢失输入 | 先运行保活回归并检查挂载次数；终端宿主改动与视觉迁移分开，可独立回退 |
| 菜单或嵌套弹窗错误吞掉按键 | 保留终端事件桥和关闭策略，补充事件顺序及真实 WebView 检查，逐个宿主迁移 |
| 表单迁移改变凭据保留或清空语义 | 使用 fake keyring/API 验证提交载荷与重置，敏感数据不进入通用缓存 |
| 查询缓存展示旧会话数据或重复执行命令 | 查询身份与会话生命周期绑定，命令显式执行，禁用默认重试并测试延迟结果 |
| 样式拆分改变级联或 Portal 展示 | 按功能迁移并删除旧规则；保留同场景截图，对比后再进入下一功能 |
| 新库适配层比原实现更复杂 | 每个试点有独立采用决定；可退回局部实现，保留已完成的状态归属和校验整理 |
| 目录搬迁掩盖业务变化 | 使用独立迁移提交，更新所有导入和配置解析，不长期保留兼容转导出 |

完成本计划需要同时满足：

- 五个问题均有已落地改进，试点依赖有明确采用或退出记录。
- 通用标签、菜单、按钮、弹窗与通知的职责和入口统一，移除已替代的状态、监听和兼容桥接。
- 终端、SFTP、Agent、主机、设置及传输流程通过针对性回归；跨平台构建与 smoke 不受影响。
- `App` 以应用组合为主，功能有明确所有权，公共控件与功能样式不再依赖不断扩大的全局文件。
- 新模块遵循受约束的导入方向；类型检查、测试、lint 和构建门禁通过。
- 更新本计划实施记录、`docs/README.md`、必要的 `AGENTS.md` 和 `tests/README.md`。未验证的平台、暂缓的性能优化和残留风险如实记录，不标记为已完成。
