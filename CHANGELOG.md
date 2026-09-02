# Changelog

## v0.3.0 — 2026-09-02

- **命令重构：`up`/`down` 合并为统一的 `cp` 命令**（对齐 tosutil）
  - 方向由位置参数顺序决定：`aos cp <本地> tos://...` 上传，`aos cp tos://... <本地>` 下载
  - 云上路径必须带 `tos://`；支持 `tos:///前缀` 简写（bucket 用配置默认值）
  - 上传目标前缀直接铺入（文件 key = 目标前缀 + 相对路径），不再自动拼目录名
  - 移除 `-spi`/`-contract`/`-name` 参数与 SPI 推导逻辑（internal/spi 删除）
  - 参数对齐 tos 习惯：`-j`（文件并发，替代 -thread）、`-f`（强制覆盖，替代 -overwrite）、`-e`（排除，替代 -exclude）
  - 移除 `-dry-run` 上传预览参数（上传前可用 `aos ls` 查看目标前缀）
  - 目录默认递归上传；移除了 up/down 子命令（原命令将提示未知命令）
  - 数据还原：单参数本地路径 `aos cp <上传时的路径>` 自动按上传记录下载回原路径（不再需要 -d/-id）
  - `aos stat` 默认只显示中断/失败与近 2 天的任务（`-a` 显示全部），避免旧的成功任务刷屏
  - 下载任务也写入记录库（新增 direction 列区分 up/down，老库自动迁移）；`stat` 增加方向与路径列（上传=本地路径，下载=tos 源路径）
  - `stat` 列按终端显示宽度左对齐（中文全角字符对齐不再错位）；时间格式简化为 `MM-DD HH:MM`；`stat -id` 展示本地/云上路径
  - 云端拷贝（tos:// → tos://）暂不支持，报错提示，后续基于 CopyObject 实现
- 修复：`ls`/`cp` 显式 `tos://bucket/...` 路径不再要求配置默认 bucket（新增 `config.ValidateAuth`）
- 修复：`cp -checkpoint` 上传的 checkpoint 文件集中存于上传根目录 `.aos/checkpoints/`，不再污染源目录
- 修复：单参数本地路径回查对单文件上传可精确还原原文件路径
- 改进：`*.tmp`/`*.checkpoint` 默认跳过时给出汇总提示，不再静默
- 改进：未知 flag 不再被吞成前一个 flag 的值；`ls` 支持单文件对象显示
- 修复：失败中断后未执行的文件不再计入"失败"统计（stat 只统计真正失败的文件）
- 修复：下载清单（`.aos/manifest.db`）写入失败不再中断任务（文件已下载，仅提示，下次按 ETag 重下）
- 修复：`-ps` 分片大小解析增加整数溢出防护
- 修复：还原下载提示不再显示已废弃的 SPI 字段
- 修复：`check` 公网回退按 region 推导（非北京区域不再误探测北京 bucket）
- 修复：`ls` 列表超时（2 分钟限制）时给出中文提示，不再直接输出 context deadline exceeded
- 文档：README 全面重写，docs/index.md 改为指向根 README 的软链

## v0.2.0 — 2026-08-31

- 仓库转为公开，启用 GitHub Pages 说明文档站（https://seqyuan.github.io/aos/）
- 建立 CI 与发版自动化：push main 自动测试并构建 Linux amd64/arm64 产物；打 `v*` 标签自动发布 GitHub Release（附带 SHA256SUMS）
- 清理仓库历史与脚本中的真实 bucket、项目编号及内部节点 IP，统一替换为虚构示例；示例配置改用公网 endpoint
