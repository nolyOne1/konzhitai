# 云令持续集成

`.github/workflows/ci.yml` 在面向 `main` 的 Pull Request、`main` 推送和人工触发时运行，只做验证，不部署、不连接生产服务器，也不读取仓库 Secrets。

五项稳定检查为：

- 后端测试与构建
- 前端测试与构建
- 端到端测试
- 代理安装与打包
- 部署配置与镜像

端到端检查只访问 Runner 上由 Playwright 启动的 Vite 服务。失败或取消时上传固定名称 `playwright-failure-diagnostics`，内容仅限 Playwright HTML 报告、截图和 Trace，保留 7 天；成功时不上传。

`main` 必须启用 Pull Request、分支保持最新、以上五项必需检查、解决审查对话、禁止强制推送和禁止删除。当前审核批准数为 0；不得给普通成员或自动化配置绕过权限。

如果修改 Job 显示名称，必须先调整分支保护中的必需检查，再提交工作流改名，避免 Pull Request 永久等待旧名称。
