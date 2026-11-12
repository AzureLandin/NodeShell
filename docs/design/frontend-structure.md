# 前端结构与维护约定

更新于 2026-11-12，记录 P7 目录迁移和 P8 本机收尾后的源码结构。阶段过程与验证结果见[前端工程改造计划](frontend-engineering-plan.md)及[P8 验收报告](../reports/ui/frontend-p8-acceptance.md)，测试命令见[测试指南](../../tests/README.md)。

## 目录与依赖

```text
src/renderer/src/
  App.tsx                  # providers、布局与功能接线
  App.css                  # 应用框架、侧栏、停靠区域
  app/                     # 弹层组合、侧栏组合、确认队列、查询 provider
  features/
    hosts/                 # 主机表单、列表查询、创建和编辑命令
    sessions/              # 连接协调、会话订阅、标签与终端
    settings/              # 偏好读写、设置界面和模型配置
    permissions/           # 后端权限请求队列和确认界面
    monitoring/            # 监控面板
    tunnels/               # 端口映射面板
    transfers/             # 传输任务与进度
    agent/                 # 对话、流式事件与工具展示
    sftp/                  # 目录、文件操作和编辑器
  ui/                      # 基础控件、公共弹层、通知和共享样式
  api/                     # Wails bridge 与类型适配
  i18n/                    # 文案
  theme.css                # 主题变量
```

每个功能通过自己的 `index.ts` 提供实际需要的公开接口。应用层和其他功能通过该入口导入，功能内可以直接导入本功能文件；不保留旧 `components/`、`hooks/` 兼容入口。测试允许直接导入被测单元。

依赖方向为 `App / app → features → ui、api、shared`。跨功能依赖只能使用公开入口，优先由应用层用参数和回调接线。功能不得导入 `app` 或 `App`，`ui` 不依赖应用组合或功能代码。Base UI 仅从 `ui/` 内直接导入。这些边界由 `tests/contracts/ui-import-boundary.test.ts` 检查。

## 状态所有权与生命周期

| 所有者 | 职责与约束 |
| --- | --- |
| `App` / `AppOverlays` | 应用布局、开关和跨功能接线；弹层 Props 从实际组件派生 |
| `settings/useAppSettings` | 八项非敏感偏好、主题和语言应用、已确认基线、乐观更新与失败回退 |
| `sessions/useConnectionFlow` | reducer 表达密码、连接、主机密钥确认和凭据提示阶段；同步 busy 门闩阻止重复命令 |
| `sessions/useSessions` | 会话列表、活动项、输出分发和后端事件订阅 |
| `hosts/useHosts` / `hosts-query` | 唯一主机列表查询；本地读取错误、刷新和已确认写入协调 |
| `hosts/useHostCommands` | 创建后连接、编辑后按需保存凭据；写入与后续刷新错误分别处理 |
| `app/use-confirmation-queue` | 应用内 FIFO 确认请求，按身份完成一次；卸载时所有等待者得到 `false` |
| `permissions/usePermissionQueue` | 后端权限请求去重、排队、提交与重试；卸载只退订，后端会话与超时策略负责请求终结 |

设置的同一字段按用户操作顺序串行写入，版本标记阻止旧响应覆盖新值，最近一次失败回退至已确认值。字体大小保留 300ms 防抖；卸载清理计时器，尚未发送的写入不再执行，已发送 IPC 的迟到结果不更新界面。晚到的初始化读取不能覆盖已编辑值或已保存基线。

确认和权限操作绑定请求 ID，旧回调不能完成下一个请求。连接协调及主机命令在异步边界检查生命周期，卸载后的结果不能继续触发主机密钥重试、凭据提示或凭据写入。前端忽略迟到结果不代表后端调用被取消。

会话 ID 保持稳定键；侧栏和会话面板使用 `keepMounted`，切换面板保留终端实例与订阅。关闭后的会话选择仍归 `useSessions` 管理。不要把终端输出、Agent 流或传输进度搬入查询缓存。

HostForm 是限定范围的 React Hook Form 试点，纯默认值、校验和 DTO 转换位于 `hosts/host-form-model.ts`。TanStack Query 仍只管理主机列表读取；设置、SFTP、端口状态不在试点内。密码和 API Key 不进入这些状态容器或日志；密码只用于短期草稿和直接命令。

## 样式归属

入口 `main.tsx` 依次加载 `theme.css`、`ui/surfaces.css`、`App.css`。公共控件按各自入口加载 `ui/*.css`，业务文件导入所属功能的 `*.module.css`。九个功能分别拥有自己的 CSS Module。

`ui/surfaces.css` 保存基础 reset、滚动条、通用 modal、表单、菜单、通知和状态样式。历史命名 `settings-modal-header` / `settings-modal-close` 同时用于主机与 SFTP 弹层，因此仍是共享样式契约。`App.css` 只维护应用框架，不添加功能专属选择器。

`bindStyles()` 将已声明的局部类转换为 CSS Module 类，保留明确的共享类。模块内引用公共 `.modal`、`.form-field`、状态类或 CodeMirror 类时显式使用 `:global(...)`；不要依赖其他功能模块的生成类名。Portal 的样式通过实际弹层类传入，不能假定其 DOM 位于功能面板内部。

P7 保留原样式值并比对迁移前后 726 组选择器和声明。该比对保护样式内容，不能证明级联、布局或原生 WebView 外观一致。Vitest 使用非作用域类名以保持交互测试选择器稳定，生产 Vite 构建仍生成作用域类名；主题、窄窗、Portal、焦点和输入法需要原生验收。

## 已知边界

P8 补齐了 P6 暂缓的面板生命周期保护。SFTP 在会话/连接身份变化时清理缓存和弹层目标；同一面板的目录工作串行执行，文件操作使用明确的绝对远端路径，失败导航后重新读取权威 cwd/list。折叠保留当前会话缓存，后台结果只允许更新其原本的会话生命周期。

端口列表读取与 start/stop 命令分别拥有版本保护，命令开始使旧读取失效；同步门闩阻止重复命令，切换会话或卸载阻止后续批量项发送。旧错误、成功和 finally 不能覆盖新会话；复制反馈计时器随生命周期清理。这些是组件内的结果隔离，不能取消已发送 IPC，也不保证其他后端调用者不会改变同一会话 cwd。

本轮未新增依赖、修改 Go API 或扩大查询试点。原生交互、Windows/Linux 打包和真实运行性能仍有未验证项，详见验收报告；jsdom 负载回归不等同于 WebView 帧率、内存或启动时间测量。
