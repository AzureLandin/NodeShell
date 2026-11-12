# 测试指南

前端测试按用途分类，测试文件统一使用 `*.test.ts` 或 `*.test.tsx` 命名。

| 目录 | 放置内容 |
| --- | --- |
| `unit/` | 纯逻辑、数据处理和 API 适配层单元测试 |
| `ui/` | React 组件、用户交互和界面流程测试 |
| `contracts/` | CI 工作流结构、运行时依赖和构建约束测试 |
| `support/` | 全局 setup、fake API、事件辅助函数和可复用测试数据 |

`support/` 只放辅助代码。新增测试应放入对应的测试目录，避免堆在 `tests/` 根目录。DOM 测试在文件顶部声明 `// @vitest-environment jsdom`，其余测试默认使用 Node 环境。UI 测试优先复用 `support/helpers.tsx` 中的 `installFakeApi`、`renderWithI18n` 和事件辅助函数。

## 运行前端测试

从仓库根目录通过 npm 脚本运行：

```bash
npm test
npm test -- tests/unit
npm test -- tests/ui
npm test -- tests/contracts
npm test -- tests/ui/settings.test.tsx
npm run test:watch
```

`npm test` 和 `npm run test:watch` 显式加载 `config/vitest.config.ts`，测试发现规则为 `tests/**/*.test.{ts,tsx}`，全局 setup 位于 `tests/support/setup.ts`。直接调用 Vitest 时需传入 `--config config/vitest.config.ts`。

类型检查运行 `npm run typecheck`，其中测试类型检查使用 `config/tsconfig.test.json`。Vite 和其他 TypeScript 详细配置也集中在 `config/`。

标签交互改动需同时验证键盘导航、`tab` 与 `tabpanel` 的关联、关闭后的选中项及焦点。终端保活测试使用真实应用流程，仅替换 xterm 等浏览器适配，检查切换不重建实例、后台输出不串流、关闭只释放目标终端；原生 WebView 的焦点和布局仍需在桌面应用中检查。

菜单回归覆盖受控关闭、完整外部点击、键盘选择、连续右键更换目标、焦点恢复和宿主生命周期。终端测试仅替换 xterm 边界，验证菜单期间按键不发送到 SSH、关闭后 Escape 正常发送。弹窗测试区分关闭请求与动画完成，验证重复请求只通知一次，并保留密码、忙碌与嵌套 Escape 策略。真实 WebView 的剪贴板、IME、边缘定位和原生焦点仍需单独验收。

通知测试通过真实 `NotificationProvider` 和 `useNotify` 发消息，不模拟管理器行为。公共层验证单条覆盖、重复消息、手动关闭、显式超时及卸载清理；`connect-flow.test.tsx` 验证连接、会话事件和设置保存共用通知入口，并保留密码弹窗内的局部错误。主机读取错误自 P6 起留在主机管理器，提供局部重试，不产生重复通知。使用 fake API 触发业务事件，关闭通知不能清除会话错误或因普通重渲染再次显示。

表单纯逻辑由 `unit/host-form-model.test.ts` 验证，公共标签 / 帮助 / 错误关联由 `ui/form-field.test.tsx` 验证；HostForm 使用真实 React Hook Form 和 Base UI，不模拟内部字段状态。交互回归保护新建 / 编辑、认证切换、整数端口、首错聚焦、敏感草稿清理、空凭据保留、重复提交、失败重试、中英文切换及迟到异步结果。HostPickerModal 验证保存期间阻止 Escape、外点和目标切换，以及取消 / 重新打开后的初值。可控 Promise 模拟延迟，不依赖外部 SSH 或文件对话框；IME 合成事件检查不能替代原生输入法验收。

查询回归使用真实 TanStack Query，每项测试独立创建客户端并清理缓存；通过可控 Promise 验证共享读取、连续刷新、写入前旧读取、卸载后的迟到结果和 StrictMode。`ui/hosts-query.test.tsx` 还覆盖离线 IPC、关闭自动重试 / 聚焦 / 联网恢复刷新、写入成功而刷新失败、命令拒绝和敏感数据不进入缓存。`ui/host-picker-modal.test.tsx` 区分加载 / 读取错误 / 空列表，覆盖删除失败与重试；`ui/connect-flow.test.tsx` 保护凭据前权威刷新、读取或标记失败不误报连接失败、后续重新提示及凭据载荷隔离。取消只保证前端忽略结果，不代表后端已停止执行。

P7 的生命周期回归集中在四个文件：`ui/app-settings.test.tsx` 检查设置回退、串行写入、迟到读取、防抖和 StrictMode；`ui/confirmation-queue.test.tsx` 检查 FIFO、重复/过期回调与卸载完成；`ui/permission-queue.test.tsx` 检查去重、关闭事件、提交失败重试和退订；`ui/connection-flow-lifecycle.test.tsx` 检查重复连接及卸载后的确认和凭据流程。`contracts/ui-import-boundary.test.ts` 同时限制 Base UI 导入、通知内部入口及跨功能深层导入。测试允许直接导入被测功能内部单元，生产代码通过功能公开入口导入。

Vitest 将 CSS Module 类名设为 `non-scoped`，让现有交互测试保持稳定选择器；生产 Vite 构建仍生成作用域类名。jsdom 不验证布局与级联。P7 另行对比了迁移前后的 726 组样式选择器/声明，这只保护样式内容；明暗主题、窄窗、Portal 和原生焦点仍需在 Wails 中验收。

P8 的 `ui/sftp-lifecycle.test.tsx` / `ui/tunnel-lifecycle.test.tsx` 用 `support/deferred.ts` 控制 IPC 完成顺序，覆盖切换/断连/卸载、目录串行与失败协调、旧 start/stop 结果和批量操作终止。SFTP 文件操作断言绝对远端路径，避免后端 cwd 已变化时命中其他目录。`session-terminal-persistence.test.tsx` 另有四会话、800 段混合 Unicode/ANSI 输出回归，核对完整分流、实例保活与释放；它使用 xterm 适配替身，不是性能基准。

`renderWithI18n` 通过稳定 wrapper 保留语言上下文，可传入第二个 i18n 实例参数。调用返回的 `rerender` 时直接传入被测组件，不再额外包一层 Provider，否则会改变组件树身份并破坏草稿保留测试。需要语言切换时操作传入的同一实例。

## Go 测试

Go 测试遵循同包放置约定，与被测代码保存在一起，例如 `internal/sessions/*_test.go`。仓库根目录的 `main_test.go` 和 `windowsize_test.go` 属于程序入口包，因此保留在根目录。集中迁入本目录会改变 Go 的包边界，影响对包内实现的测试。

```bash
go test ./internal/sessions -count=1
npm run build:wails
go test . -count=1
```

根包测试依赖 `main.go` 嵌入的 `frontend/dist/`，因此需先构建前端。完整检查顺序和平台要求见根目录 `AGENTS.md`。历史报告中的旧目录描述保留当时的上下文，当前目录约定以本指南为准。

## 运行产物与临时脚本

正式测试、公共辅助代码、必要的测试数据以及 CI 使用的 smoke 和打包脚本随代码一起追踪。个人一次性调试脚本放在 `scripts/local/`，该目录已加入 `.gitignore`；需要团队复用的脚本应放回受版本控制的目录。

覆盖率报告统一放在根目录 `coverage/`，测试运行结果放在根目录 `test-results/`。根目录的 `coverage.out`、`coverage.txt`、`*.coverprofile`、`*.prof` 和 `*.pprof`，以及 Go 测试二进制 `*.test`、`*.test.exe` 和 TypeScript 缓存 `*.tsbuildinfo` 都已忽略。前端构建目录、Wails 打包产物、日志和 ESLint 缓存沿用现有忽略规则。
