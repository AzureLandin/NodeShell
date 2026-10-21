# 文档索引

本目录保存 NodeShell 的设计文档与历史实施报告。当前行为以根目录 `README.md`、代码和 `.github/workflows/ci.yml` 为准。

## 设计文档

- [Wails 重构设计](design/wails-rebuild.md)

## 历史报告

报告按功能域归档，文件名保持不变；报告之间的相互链接已改为相对路径。

| 目录 | 内容 |
| --- | --- |
| [reports/agent](reports/agent/) | Agent 供应商、模型与工具调用 |
| [reports/mcp](reports/mcp/) | MCP 权限与外部客户端集成 |
| [reports/terminal](reports/terminal/) | 多会话终端与 xterm.js 优化 |
| [reports/transfer](reports/transfer/) | SFTP、文件传输任务中心与拖放上传 |
| [reports/ui](reports/ui/) | 侧栏、端口映射、设置与主机管理界面 |

这些报告记录当时的方案、实施过程和验收结果，不代表当前实现；引用前请核对代码。
