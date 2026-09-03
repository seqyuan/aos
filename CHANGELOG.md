# Changelog

## v0.4.5 — 2026-09-03

- **修复：`cp` 上传使用 `tos://` 路径里的 bucket**，不再误用配置默认桶（与 `ls` / `rm` / 下载对齐；未配默认桶时也可按路径桶上传）
- **修复：下载还原软链接**
  - 只匹配 `status=done` 的 up 任务（忽略 running / break）
  - 先建临时 symlink 再 rename，避免 `Remove` 成功但 `Symlink` 失败时丢掉已下载文件
  - `Rel` 走 `SafeJoin`，拒绝绝对路径、不会改写下载目录之外的文件
  - 兼容 v0.4.4 之前任务库里的裸前缀记录
  - 解析路径或打开任务库失败会提示「跳过还原软链接」；无匹配 up 任务仍保持静默
- **修复：`cp` 只认 `tos://` 为云路径**（`http://` / `s3://` 不再被当成 TOS）
- **修复：`rm tos://bucket -r` 整桶删除**——确认提示标明「整个 bucket」；`-f` 仍会先打印警告
- **修复：分片 abort 失败以非零退出码退出**（与对象删除失败一致）
- **修复：下载跳过同时比对本地文件大小**，截断/损坏文件会重下；已还原的 symlink 仍按存在即跳过
- **修复：`-j` 超出 16 时夹死**（与 README「最多 16」一致）
- **文档**：README 补充整桶删除警告、分片 abort 失败退出码、下载跳过比 size 的说明

## v0.4.4 — 2026-09-03

- **行为变更：`cp` 上传软链接默认转同名文本文件并记入数据库**（恢复 v0.2 设计）
  - 默认（不加 `--follow-links`）：目录递归/单文件源遇到软链接（含断链）不再跳过，改为上传**同名文本文件**（内容 = readlink 原值），并把链接明细（相对路径 / 目标地址 / 对象 key）写入任务库新增的 `task_links` 表
  - `--follow-links`：维持现状——溯源上传链接目标的真实内容，不记录到数据库
  - 下载侧：下载完成后自动把文本文件还原为 symlink——单参数 `aos cp <本地路径>` 按上传记录还原；普通 `aos cp tos://... <目录>` 按 `tos://bucket/前缀` 匹配最近的 up 任务还原
  - up 任务现在记录完整 `tos://bucket/前缀`（此前只记裸前缀），便于下载侧按前缀匹配
  - 数据库新增 `task_links` 表（`link_rel` / `link_target` / `object_key` / `size`），`stat -id` 可直接查询；老库无需迁移（新表随首次打开自动创建）
- **文档**：README 新增「软链接：要不要加 `--follow-links`？」章节，说明两种模式的对比与选择

## v0.4.3 — 2026-09-03

- **新功能：`rm` 命令**——删除 TOS 对象（参考 tosutil rm，不做删桶）
  - 单对象删除：`aos rm tos://bucket/dir/file.txt`——精确 key 直接删、幂等（不存在视为成功）、不询问
  - 递归删除：`aos rm tos://bucket/dir -r` 删除前缀下所有对象并顺带清理该前缀未完成的分片上传任务；删除前提示数量并 y/N 确认（`-f` 跳过；非终端默认拒绝，防脚本误删）
  - 批量删除并发执行（分批 ≤1000），单个失败不中断、完成后报告对象删除总数与分片清理数
  - 路径必须为 `tos://` 开头的云上路径；开启版本控制的 bucket 上删除仅生成 delete marker（S3 语义）
- **文档**：README 新增「安装」章节（Release 下载 / go install / go build 三种方式）；docs 站点改为构建时从根 README/CHANGELOG 复制生成（兼容 fresh checkout 下 docs/ 目录缺失）；mkdocs 排除 plans/ 设计文档目录

## v0.4.1 — 2026-09-03

- **破坏性变更**：命令行 flag 迁移到 pflag，多字符选项由单横线改为双横线（`-ps`→`--part-size`、`-db`→`--db`、`-id`→`--id`、`-ak`/`-sk`→`--ak`/`--sk`、`-limit`→`--limit`、`-max-depth`→`--max-depth`、`-no-record`→`--no-record`、`-follow-links`→`--follow-links`、`-checkpoint`→`--checkpoint`、`-no-checkpoint`→`--no-checkpoint`、`-timeout`→`--timeout`、`-config`→`--config`、`-endpoint`→`--endpoint`、`-region`→`--region`、`-bucket`→`--bucket`）；单字符保留 shorthand（`-j -p -q -f -e -m -a`）并新增长名（`--job --part-task --quiet --force --exclude --mod --all`）
- **改进**：flag 与位置参数混排、负数参数、防吞值改由 pflag 原生支持，删除约 50 行手工 reorderArgs 逻辑
- **改进**：版本号单一来源改为 git tag（`main.go` 默认 `dev`，Makefile 用 `git describe --tags` 动态获取），发版只打 tag 不再改文件内版本号
- **修复**：溯源链接文件在任务失败后被静默丢弃，现计入「跳过」并在总结中提示
- **修复**：pflag 在 ContinueOnError 下不自动打印解析错误，显式打印避免非法参数静默失败
- **文档**：文档页（docs/index.md、docs/changelog.md）改为构建时从根 README/CHANGELOG 复制，消除软链在 Windows/CI 下的跨平台脆弱性
- **依赖**：`go` 指令去掉 patch 强制（`go 1.26.5`→`go 1.26`）；新增 `github.com/spf13/pflag v1.0.10`

## v0.3.1 — 2026-09-03

- 修复：下载跳过已完成文件不再逐条打印（避免大目录二次下载刷屏），改为聚合汇总；`-q` 安静模式下不输出该汇总
- 修复：CRC 失败自愈的临时文件清理匹配 SDK 实际命名（`<目标文件>.temp`），此前按带时间戳后缀匹配永不命中
- 改进：`aos cp -no-record` 帮助文案明确上传/下载均可用；`aos stat` 帮助文案由“上传历史”改为“传输历史”（上传/下载均记录）
- 文档：README 修正多后端接入表述（当前绑定火山云 TOS，传输层集中在 `internal/tosx`）、单文件上传对象 key 语义（目标路径整体即 key）、`-db` 默认路径优先级、配置文件查找顺序；`aos.json.example` endpoint 去掉 `https://` 前缀

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
