# 前端工程改造 P8 验收记录

日期：2026-11-12。范围：P1—P7 后的最终回归、残留竞态修复、本机构建和验收边界整理。

**结论：本机代码与自动门禁通过；原生交互、其他平台和真实性能验收尚未完成，整份改造计划仍有发布验收项。** 本轮没有创建提交、推送分支、触发远端 CI 或发布版本。

## 环境与验证方法

- macOS ARM64；最终前端检查使用临时 npm 缓存中的 Node 22.23.5，保持 CI 的 Node 22 主版本；npm 11.20.0。没有修改项目 manifest 或 lockfile 的 P7 状态，也没有切换全局 Node。
- Go 使用 `GOTOOLCHAIN=go1.26.0`，Wails CLI 2.13.0。默认 Go 1.27.4 的 Wails 包分析问题在 P7 已复现；本次沿用项目 Go 1.26，未升级 Go 依赖。
- 正式测试保留在 `tests/`，通过 fake IPC、可控 Promise、真实 React/Base UI/RHF/Query 交互验证；xterm 等适配边界使用替身。Go 使用进程内 SSH 测试服务，不依赖公网 SSH。
- 逐包 Go 测试使用 `-count=1 -timeout 90s`，过滤 `node_modules`，没有启用 race。最终前端再次构建后重新运行 vet 和根包测试；未改变的内部包无需重复。

## 本轮修复

### SFTP 生命周期与目录顺序

P7 保留的实现在首次读取未完成时断连、折叠期间换会话、旧命令结束后刷新等场景中，不能完全隔离旧结果。P8 为会话/连接身份建立生命周期，切换身份时清空旧列表、加载状态、选择、拖放和弹层目标；成功、错误及 finally 都检查原生命周期。

面板内 `chdir`、cwd/list 和后续刷新串行执行，防止本地并发读取与有状态 cwd 交错。文件动作使用绝对条目路径，目录输入及新建/重命名目标根据当时显示的 cwd 固定为绝对路径。编辑目标同时绑定会话，不能在切换后的新会话先读取旧目标。较晚导航失败后重新读取权威 cwd/list，保留原错误，避免较早导航已经改变后端 cwd 而界面仍展示旧目录。

折叠本身保留当前会话缓存。这些保护只约束当前面板，不能取消已发出的 IPC，也不保证其他后端调用者不会改变相同会话 cwd；后端 POSIX 规范化和安全校验保持原有规则。

### 端口映射生命周期与命令

discover/list 的结果按会话生命周期和读取版本接受。命令开始使此前读取失效；同步 pending 门闩防止 React 重绘前的重复 start/stop，整个批量操作保持命令所有权。切换、断连或卸载后不再发送剩余批量项，旧成功/失败/finally 不能更新新会话或解除新命令的忙碌状态。选择和复制提示在身份变化时清理，复制计时器在卸载时清理。

已经发送的 start/stop 保留其后端结果，不因面板消失自动发送反向操作。切回原会话时从后端重新 discover/list。

### 工程与测试收尾

- 修复 Agent 的不稳定空列表和模型选择 effect 依赖，清理剩余格式警告。API adapter 和共享 DTO 仅格式化；另用 TypeScript token 对比确认没有契约变更。
- `renderWithI18n` 用稳定 wrapper 保留语言上下文，支持注入同一 i18n 实例。HostForm 重渲染测试直接更新被测组件，避免新增 Provider 造成重挂载，让草稿保留断言覆盖实际行为。
- 新增 SFTP 8 项、端口映射 6 项回归，以及四会话/800 段 Unicode、emoji、ANSI 混合输出回归，共增加 15 项，测试总数从 405 增至 420。负载回归检查完整分流、实例不重建、关闭只释放一次及迟到输出不写入已释放终端。
- 核对 Wails 生成的 `build/darwin/Info.plist` 与其自带默认模板一致后，仅忽略该生成路径；应用图标和团队自定义模板仍需按用途追踪，未删除正式测试或构建脚本。
- 更新架构约定、测试指南、AGENTS 与计划。没有新增依赖、改动 Go/API 或修改 CI；没有调用子代理。

## 自动检查与本机产物

| 检查 | 结果 | 证据/边界 |
| --- | --- | --- |
| 类型检查 | 通过 | Node 22 下 node/web/test 三组 TypeScript 配置 |
| 前端测试 | 通过 | 38 文件、420 项；已有 Canvas `getContext()` 提示保留 |
| lint | 通过 | 0 errors / 0 warnings，不通过禁用规则隐藏警告 |
| 生产前端构建 | 通过 | 1014 modules；CSS Modules 生产作用域保持启用 |
| Go vet | 通过 | Go 1.26；最终构建后再次检查 |
| Go tests | 通过 | 无测试缓存的 22 包检查，21 包含测试，sshtest 无独立测试 |
| macOS ARM64 Wails | 通过 | 编译、应用打包与 ad-hoc 自签名；未启动窗口做交互 |
| MCP smoke | 通过 | 最终原生程序；initialize、10 个工具、ping；3 响应，stderr 为空 |
| macOS DMG / ZIP | 通过 | 现有脚本打包；ZIP 完整性、DMG 校验和及应用签名校验通过，未公证或发布 |
| 差异与产物 | 通过 | `git diff --check`；dist、二进制和默认生成模板均受忽略规则保护 |

最终前端构建后执行的命令核心如下：

```bash
GOTOOLCHAIN=go1.26.0 wails build -skipbindings -s -m -nosyncgomod -nocolour
bash scripts/mcp-smoke.sh build/bin/nodeshell.app/Contents/MacOS/nodeshell
bash scripts/package-macos.sh build/bin/nodeshell.app build/bin NodeShell-2.2.0-macos-arm64
```

`-s` 复用先前通过的前端构建，`-m -nosyncgomod` 保持依赖文件。`.app`、DMG 和 ZIP 都是本机构建产物，位于忽略的 `build/bin/`，不加入版本控制。

产物为 `NodeShell-2.2.0-macos-arm64.zip`（约 7.4 MiB）及 `.dmg`（约 8.4 MiB）。`codesign --verify --deep --strict`、`unzip -tq` 和 `hdiutil verify` 均通过；ZIP 内程序与当前构建程序按 SHA-256 比对一致。现有打包命令产生 `hdiutil create` 弃用提示，但镜像创建和校验成功，未修改打包脚本。

## 体积与性能边界

从 `git archive HEAD` 在临时目录提取改造前源码，复用同一套已安装依赖和 Node 22.23.5 重建。临时基线保留原 Vite 配置路径，当前版本使用 `config/vite.config.ts`；两者均生产构建。以相同工具链比较，避免将此前 Node 26 的 gzip 数字直接与 Node 22 混用。

| 产物 | 改造前基线 | P8 最终 | 变化 |
| --- | --- | --- | --- |
| JS | 2,424.41 kB | 2,530.05 kB | +105.64 kB |
| JS gzip | 776.24 kB | 809.75 kB | +33.51 kB |
| CSS | 83.63 kB | 91.09 kB | +7.46 kB |
| CSS gzip | 14.66 kB | 16.32 kB | +1.66 kB |

变化包括 P1—P8 的组件封装、RHF/Query 试点、功能作用域类名及竞态防护。原有大于 500 kB chunk 提示仍存在；本轮不以调整警告阈值或未经验证的拆包宣称优化。保留试点的理由是已验证的状态归属与错误处理收益，不能据此证明启动开销可接受。

四会话/800 段输出是 jsdom 中的正确性与资源释放回归，xterm 渲染被替换；没有记录原生 FPS、渲染耗时、RSS 或启动时间。SFTP 大目录、Agent 长对话、多传输任务的同场景基线/最终性能采样仍未完成，不据此引入虚拟化或新表格/图表依赖。

## 待完成验收与归属

| 项目 | 当前状态 | 后续执行位置与通过条件 |
| --- | --- | --- |
| macOS 原生主题/布局/语言 | 工具受限，未验证 | 本机 Wails：明暗/系统主题、中英长文、窄窗和缩放，无溢出及 Portal 级联变化 |
| 原生输入与弹层 | 工具受限，未验证 | 本机 Wails：Tabs/关闭焦点、嵌套 Escape、菜单后远端 Escape、剪贴板、IME、拖放各场景实际验证 |
| Windows 包与 MCP | 未运行 | 现有 Windows 原生 CI runner；保持 `-windowsconsole`，构建、NSIS、smoke 与交互验收 |
| Linux 包与 MCP | 未运行 | 现有 Linux 原生 CI runner；webkit2gtk 4.1 标签、四种包、smoke 与交互验收 |
| 原生性能基线/最终 | 未采样 | 对相同规模多会话、SFTP 大目录、Agent 长对话及多任务采样，记录启动/耗时/内存与环境 |
| 正式签名、公证与发布 | 未执行 | 使用现有 CI 和项目发布流程；本地产物为 ad-hoc 验证包 |

本次浏览器枚举返回 `The browser access host is unavailable`；请求本地 NodeShell 应用访问返回 `Computer Use was not approved to use NodeShell`。没有绕过这些限制或把截图/交互记为通过，也没有修改用户数据来制造验收环境。源码和自动门禁已收尾，上表保留为 P8 发布验收项，后续凭实际结果关闭，不能将 P8 整体标为验收通过。
