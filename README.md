# NodeShell

NodeShell 是一款跨平台桌面 SSH 客户端，面向需要同时管理多台远程主机、进行终端操作、SFTP 文件传输和远程运维的开发者与运维人员。

当前版本：**2.1.0**

[![CI](https://github.com/AzureLandin/NodeShell/actions/workflows/ci.yml/badge.svg)](https://github.com/AzureLandin/NodeShell/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/AzureLandin/NodeShell?display_name=tag)](https://github.com/AzureLandin/NodeShell/releases/latest)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 功能概览

- **SSH 主机管理**
  - 新增、编辑、删除和快速连接主机
  - 支持密码、keyboard-interactive 和私钥认证
  - 支持首次主机密钥确认与主机密钥变更警告
  - 主机配置和 known-hosts 数据采用原子写入，降低配置损坏风险

- **多会话终端**
  - 支持多个 SSH 会话标签页
  - 基于 [xterm.js](https://xtermjs.org/) 的交互式终端
  - 终端使用 `xterm-256color` PTY，支持彩色输出、全屏程序和终端尺寸同步
  - 后台会话保持终端实例和输出，切换会话时不会清屏
  - 支持终端字体、字号和明暗主题设置

- **SFTP 文件管理**
  - 浏览、切换远程目录
  - 新建目录、重命名和删除远程文件/目录
  - 上传、下载和拖放上传
  - 支持在内置编辑器中读取和编辑远程文本文件
  - 本地文件路径受到用户 Home 目录边界保护

- **文件传输任务中心**
  - 独立的右上角任务面板，可随时展开或收起
  - 多文件分别显示上传/下载进度
  - 显示文件名、方向、百分比、已传输/总大小、速度和预计剩余时间
  - 支持取消、失败重试和清理历史任务
  - 已完成任务会按状态自动隐藏，仍可在任务面板中查看近期记录

- **远程主机监控**
  - 监控远端 Linux 主机的 CPU、内存、Swap、负载、网络和进程信息
  - 监控面板与当前 SSH 会话关联，切换会话时自动更新
  - 远端不支持相关命令时显示错误或空状态，不影响终端连接

- **本地端口映射**
  - 发现远端监听端口
  - 创建、查看和停止本地端口转发
  - 转发生命周期与 SSH 会话关联，关闭会话时自动清理

- **内置 Agent 助手**
  - 支持添加多个 OpenAI 兼容模型供应商
  - 每个供应商可配置接口地址、模型 ID 和 API Key
  - API Key 保存于操作系统钥匙串，不写入普通配置文件
  - Agent 可基于当前 SSH 会话执行命令、读取/写入远程文件和进行文件传输
  - 支持每次询问、允许或拒绝等敏感操作权限策略

- **原生 MCP 服务**
  - 使用同一个 NodeShell 可执行文件通过 `--mcp` 启动 MCP stdio 服务
  - 支持注册到 Cursor、Claude Code、Codex 和 OpenCode
  - 内置 10 个 MCP 工具：
    - `list_hosts`
    - `list_sessions`
    - `connect_host`
    - `disconnect_session`
    - `run_command`
    - `sftp_list`
    - `sftp_read`
    - `sftp_write`
    - `sftp_upload`
    - `sftp_download`
  - 默认可由外部 MCP 客户端负责用户确认；NodeShell 仍会校验路径、主机密钥、会话和参数边界

- **本地化与主题**
  - 支持中文和英文界面
  - 支持浅色、深色和跟随系统主题
  - 支持系统字体枚举和终端字体设置

## 下载与安装

请前往 [GitHub Releases](https://github.com/AzureLandin/NodeShell/releases) 下载最新版本。

当前 `v2.1.0` 提供以下主要产物：

| 平台 | 架构 | 产物 |
| --- | --- | --- |
| Windows | x64 | `nodeshell-amd64-installer.exe` |
| macOS | ARM64 | `NodeShell-2.1.0-macos-arm64.dmg`、`.zip` |
| Linux | x64 | `NodeShell-2.1.0-linux-amd64.AppImage`、`.deb`、`.pkg.tar.zst` |

> macOS 当前发布产物面向 Apple Silicon（ARM64）。发布产物是否经过平台签名或公证，请以对应 Release 页面为准。

### Windows

运行 Windows 安装程序并按向导完成安装。安装后可从开始菜单或桌面启动 NodeShell。

### macOS

打开 DMG，将 NodeShell 拖入 `Applications`。如果下载的是 ZIP，则解压后将应用移动到 `Applications`。

### Linux

AppImage：

```bash
chmod +x NodeShell-2.1.0-linux-amd64.AppImage
./NodeShell-2.1.0-linux-amd64.AppImage
```

Debian/Ubuntu：

```bash
sudo apt install ./NodeShell-2.1.0-linux-amd64.deb
```

Arch Linux 或兼容发行版：

```bash
sudo pacman -U NodeShell-2.1.0-linux-amd64.pkg.tar.zst
```

## 快速开始

1. 启动 NodeShell，在主机管理界面添加 SSH 主机。
2. 填写主机名称、地址、端口、用户名和认证方式。
3. 使用密码或私钥连接；首次连接时根据指纹确认主机密钥。
4. 连接成功后，可以在终端、SFTP、监控和端口映射面板之间切换。
5. 如需使用 Agent，打开设置添加 OpenAI 兼容供应商，保存 API Key 并选择默认模型。
6. 如需接入外部 Agent，在设置的 MCP 区域检查并注册 NodeShell MCP 服务。

## MCP 配置

NodeShell 的 MCP 服务使用标准输入/输出通信。手动配置示例：

```json
{
  "mcpServers": {
    "nodeshell": {
      "command": "/absolute/path/to/nodeshell",
      "args": ["--mcp"]
    }
  }
}
```

Windows 示例：

```json
{
  "mcpServers": {
    "nodeshell": {
      "command": "C:\\Program Files\\NodeShell\\nodeshell.exe",
      "args": ["--mcp"]
    }
  }
}
```

也可以在 NodeShell 设置中使用 MCP 注册功能，将本机可执行文件路径和 `--mcp` 参数写入支持的客户端配置。注册过程会尽量保留目标配置中的其他 MCP 服务和字段。

启动 MCP 服务后，NodeShell 不会初始化桌面 WebView，也不会向 stdout 输出非协议内容：

```bash
/path/to/nodeshell --mcp
```

## 安全边界

- SSH 主机密钥在认证前校验；未知或变更的主机密钥需要用户明确确认。
- 密码和 Agent API Key 优先存储于操作系统钥匙串，不写入普通 JSON 配置文件。
- 本地文件上传、下载和 MCP 文件工具限制在用户 Home 目录边界内，拒绝路径逃逸。
- Agent 的命令执行、远程写入、上传和下载可配置为每次询问、允许或拒绝。
- 外部 MCP 客户端可负责 MCP 工具确认，但 NodeShell 自身的路径、主机密钥、会话和参数校验始终生效。
- 应用退出时会清理 SSH 会话、SFTP 客户端、监控轮询、端口转发和 Agent 会话等关联资源。

## 技术栈

- **桌面框架**：[Wails v2](https://wails.io/)
- **后端**：Go
- **前端**：React、TypeScript、Vite
- **终端**：[xterm.js](https://xtermjs.org/)
- **远程连接**：`golang.org/x/crypto/ssh`
- **文件传输**：`github.com/pkg/sftp`
- **编辑器**：CodeMirror 6
- **MCP**：原生 Go stdio 服务
- **凭据存储**：操作系统 Keyring

项目采用 Go 后端领域服务与 React 前端适配层分离的结构。Wails 负责桌面窗口、Go/前端绑定和运行时事件，前端通过 `window.api` 适配层访问主机、会话、SFTP、监控、Agent、MCP 和传输任务等能力。

## 从源码运行

### 环境要求

- Go **1.26** 或兼容版本
- Node.js **22** 或兼容版本
- npm
- 使用桌面开发或打包时，需要安装 [Wails v2](https://wails.io/docs/gettingstarted/installation)
- Linux 构建还需要 GTK/WebKitGTK 开发依赖

### 安装依赖

```bash
npm ci
go mod download
```

### 启动前端开发服务器

```bash
npm run dev:wails
```

该命令启动 Vite 前端开发服务器。若要启动完整的 Wails 桌面开发环境，请使用：

```bash
wails dev
```

### 构建前端

```bash
npm run build:wails
```

### 构建桌面应用

```bash
wails build -skipbindings
```

不同平台的完整打包参数由 `.github/workflows/ci.yml` 和 `scripts/` 下的打包脚本维护。发布 CI 当前覆盖 Windows x64、macOS ARM64 和 Linux x64。

### 启动 MCP stdio 服务

```bash
go run . --mcp
```

构建完成后，也可以直接运行生成的可执行文件：

```bash
./build/bin/nodeshell --mcp
```

## 测试与质量检查

运行前端测试：

```bash
npm test
```

运行 Go 测试：

```bash
go test ./...
```

运行带竞态检测的 Go 测试：

```bash
go test -race -count=1 ./...
```

运行 TypeScript 类型检查：

```bash
npm run typecheck
```

运行 ESLint：

```bash
npm run lint
```

运行 Go 静态检查：

```bash
go vet ./...
```

CI 会在 Windows、macOS 和 Linux 原生 runner 上执行类型检查、前端测试、Lint、前端构建、Go Vet、Go 测试、Wails 构建和 MCP smoke test。

## 项目结构

```text
.
├── app.go                       # Wails 应用 facade 与领域服务绑定
├── main.go                      # GUI / --mcp 双入口
├── internal/
│   ├── agent/                   # 内置 Agent 服务
│   ├── credentials/             # 凭据与 Keyring 抽象
│   ├── hosts/                   # 主机配置存储
│   ├── mcpcli/                  # 原生 MCP runtime、工具和 stdio server
│   ├── mcpregistration/         # 外部 MCP 客户端配置注册
│   ├── monitor/                 # 远端 Linux 监控
│   ├── permission/              # 内置 Agent 权限控制
│   ├── sessions/                # SSH 会话、PTY 和终端输出
│   ├── sftpservice/             # SFTP 与文件传输任务
│   └── tunnel/                  # 本地端口转发
├── src/
│   ├── renderer/                # React/Vite 前端
│   ├── shared/                  # 前后端共享类型和错误定义
│   └── ...
├── scripts/                     # MCP smoke test 和平台打包脚本
├── tests/                       # 前端和适配层测试
├── docs/                        # 设计与重构文档
└── wails.json                   # Wails 构建配置
```

## 数据与配置

NodeShell 使用操作系统的应用数据目录保存主机、设置和 known-hosts 等非敏感数据；具体目录由运行平台决定。凭据和 Agent API Key 使用操作系统 Keyring 保存。

NodeShell 不会解密或复用旧 Electron 版本的 `safeStorage` 密文凭据。升级后如需保存凭据，请重新输入并保存；旧凭据文件会保留，便于回退旧版本。

## 贡献

欢迎提交 Issue、改进建议和 Pull Request。提交代码前建议运行：

```bash
npm run typecheck
npm test
go test ./...
go vet ./...
npm run lint
npm run build:wails
```

涉及 SSH、SFTP、终端、权限或 MCP 行为的改动，请同时补充对应的单元测试或集成测试，并说明测试环境和复现步骤。

## 许可证

本项目基于 [MIT License](LICENSE) 开源。

Copyright (c) 2026 AzureLandin
