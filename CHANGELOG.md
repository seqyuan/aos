# Changelog

## v0.2.0 — 2026-08-31

- 仓库转为公开，启用 GitHub Pages 说明文档站（https://seqyuan.github.io/aos/）
- 建立 CI 与发版自动化：push main 自动测试并构建 Linux amd64/arm64 产物；打 `v*` 标签自动发布 GitHub Release（附带 SHA256SUMS）
- 清理仓库历史与脚本中的真实 bucket、项目编号及内部节点 IP，统一替换为虚构示例；示例配置改用公网 endpoint
