# aos

对象存储上传 / 下载 / 浏览命令行工具（Go 编写，单二进制分发）。当前后端为火山云 TOS：endpoint / region / bucket / AK/SK 放在配置文件里。传输实现集中在 `internal/tosx`（当前绑定火山云 TOS SDK），未来接入 S3 等其他对象存储时在该层抽取后端接口即可，命令行与配置层无需改动。

## 特性

- **`cp`**：上传 / 下载统一命令，方向由位置参数顺序决定（云上路径带 `tos://` 前缀，与 tosutil 心智一致）
- **`ls`**：以目录树形式列出目标路径下的文件（带大小、数量统计）
- **`rm`**：删除对象——单对象直接删（幂等）；`-r` 递归删除前缀下所有对象并顺带清理未完成分片上传任务；`-f` 跳过确认
- **`stat`**：查询传输历史（上传/下载均记录；默认只显示中断/失败与近 2 天的任务，`-a` 显示全部）；`--id` 查看单次任务详情
- **`check`**：连接与权限诊断（按 region 自动尝试公网 endpoint 回退）
- **`config`**：查看 / 配置凭据

## 安装

需要 Go 1.26+。三种方式任选其一，安装后把 `aos.json`（凭据文件）放到 **aos 二进制同目录**即可使用（随二进制一起拷贝到任何机器都可用，见「配置」）。

**方式一：GitHub Release 下载（推荐）** —— 二进制自带正确版本号，附 SHA256SUMS 校验：

https://github.com/seqyuan/aos/releases （Linux 产物 `aos-linux-amd64` / `aos-linux-arm64`）

**方式二：go install 装到 `$GOBIN`**（默认 `~/go/bin`，确保其在 PATH）：

```bash
# 远程安装最新 tag 版本（@latest = 最新 git tag）
# 注意：go install 不注入版本号，aos version 会显示 dev
# 在线说明：https://seqyuan.github.io/aos/
go install github.com/seqyuan/aos/cmd/aos@latest

# 或在 clone 的仓库内编译并注入 git 版本号（推荐，version 正确显示）
go install -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty)" ./cmd/aos
```

**方式三：本地源码编译**：

```bash
go build -o aos ./cmd/aos   # version 显示 dev；要带版本号用 make build（自动取 git tag）
```

## 快速开始

```bash
# 查看配置（凭据文件与二进制同目录，随二进制一起拷贝即可使用）
./aos config

# 查看目录树
./aos ls tos://example-bucket/ACME2026001
./aos ls tos:///ACME2026001/PM-ACME2026001-01/dataset    # tos:/// 前缀用配置默认 bucket

# 上传：本地在前，目标为 tos:// 前缀（目录默认递归，内容直接铺入目标前缀）
./aos cp /path/project1/dataset tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset
./aos cp ./dataset tos:///ACME2026001/PM-ACME2026001-01/dataset

# 单文件上传（目标路径整体即对象 key，须写到文件名）
./aos cp dataset.zip tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset.zip

# 下载：tos 在前，目标为本地目录（源前缀下的对象按相对路径落盘）
./aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset /local/path
./aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset   # 省略目标 = 当前目录

# 还原上次上传的数据（单参数本地路径，自动按上传记录下载回原路径）
./aos cp /data/project1/dataset

# 任务状态
./aos stat                                      # 中断/失败 + 近 2 天的任务
./aos stat -a                                   # 全部任务
./aos stat --id 3                               # 某次任务的详情（错误信息等）
./aos stat --limit 50                           # 最多列出 50 条

`stat` 列表列：ID / 方向（up 上传、down 下载）/ 状态 / 文件进度 / 开始时间 / 完成或状态 / 路径（上传=本地路径，下载=tos 源路径）。

# 删除对象（单对象直接删、幂等；-r 递归删前缀下所有对象 + 清理孤儿分片，-f 跳过确认）
./aos rm tos://example-bucket/ACME2026001/dataset.zip
./aos rm tos://example-bucket/ACME2026001 -r
./aos rm tos://example-bucket/ACME2026001 -r -f

# 诊断连接与权限
./aos check

# 版本号
./aos version
```

示例中的合同号 `ACME2026001`、子项目编号 `PM-ACME2026001-01`、bucket `example-bucket` 均为虚构，请换成实际值。

## 配置

配置文件 `aos.json` 位于 **aos 二进制所在目录**，随二进制一起拷贝即可在任何机器使用。查找顺序：命令行 `--config` 参数 → `AOS_CONFIG` 环境变量 → 二进制同目录 → 当前工作目录（最后一项仅为便于开发调试）：

```json
{
  "endpoint": "tos-cn-beijing.volces.com",
  "region": "cn-beijing",
  "bucket": "example-bucket",
  "access_key_id": "AKLT...",
  "secret_access_key": "WXpa..."
}
```

- **endpoint 说明**：
  - 内网 / 专线环境：`tos-cn-beijing.ivolces.com`（走火山云 VPC 内网，公网不可达）
  - 公网环境：`tos-cn-beijing.volces.com`
- 修改配置：`./aos config set --ak AKLT... --sk WXpa... [--endpoint ...] [--region ...] [--bucket ...]`
- 查看配置文件实际路径：`./aos config path`
- 可用环境变量覆盖（便于 CI）：`AOS_AK` / `AOS_SK` / `AOS_ENDPOINT` / `AOS_REGION` / `AOS_BUCKET`，或 `AOS_CONFIG` 指定配置文件路径
- 单次命令覆盖：`--endpoint` / `--region` / `--bucket` 参数

## 路径规则

| 输入 | 解析结果 |
| --- | --- |
| `tos://example-bucket/ACME2026001` | bucket=`example-bucket`, prefix=`ACME2026001/` |
| `tos:///ACME2026001/PM-ACME2026001-01/dataset` | bucket 用配置默认，prefix=`ACME2026001/PM-ACME2026001-01/dataset/` |
| `example-bucket/ACME2026001`（仅 ls，首段等于默认 bucket） | bucket=`example-bucket`, prefix=`ACME2026001/` |
| `ACME2026001/PM-ACME2026001-01/dataset`（仅 ls） | 纯前缀，使用默认 bucket |

`cp` 的云上路径必须带 `tos://`（否则按本地路径处理）；`tos:///` 表示 bucket 用配置默认值。

## 上传语义

- 目标前缀**直接铺入**：本地目录 `./dataset` 下的每个文件，其 key = 目标前缀 + 相对路径
- 目录默认递归上传；单文件上传时目标路径整体即对象 key（bucket 之后的全部内容，通常以文件名结尾）
- 上传/下载任务均写入 SQLite（时间、方向 up/down、本地路径、远端路径、文件/字节进度、状态 running/done/break、错误信息），便于 `aos stat` 查看；`aos cp <本地路径>` 按上传记录还原（含软链接文本文件的 symlink 还原）
- `--no-record` 可显式跳过记录
- Ctrl+C / 报错退出时任务标记为 `break`，正常完成标记为 `done`

## 下载进度（`.aos/manifest.db`）

`cp` 下载时在落盘根目录写入 `.aos/manifest.db`。每个对象下载成功后记录 `object_key` + `etag`（云上内容指纹）。再次下载时：

- 清单里有该 key、ETag 与本次 List 一致、本地文件还在、且大小与远端一致 → 跳过
- 不在清单、ETag 变了、本地文件被删或被截断 → 重下
- 半截文件不会写入清单（清单只记录完整下载的对象）；单个大文件中途进度由分片 checkpoint 续传（默认开启，见「断点续传与完整性」）
- `-f` 忽略清单，全部重下并更新记录
- 上传默认跳过 `.aos`，不会把清单传回远端

## 软链接：要不要加 `--follow-links`？

项目里常见软链接：指向外部共享大文件（参考基因组、外部数据）、指向别的目录、或做成相对路径别名。上传前先想清楚一件事：**你要“记录这里有个链接、指向哪里”，还是“把链接指向的内容一起带走”？**

- 想**记录链接本身**（默认，不加参数）：链接被转成同名文本文件上传，内容写入链接目标地址，并把链接明细记入任务库 `task_links`；下载时自动把文本文件还原成 symlink
- 想**上传链接指向的真实内容**（加 `--follow-links`）：读链接目标的真实内容并上传（key 仍用链接在项目中的路径），但不记库、下载后不还原

### 两种模式对比

| 维度 | 默认（不加） | `--follow-links` |
|---|---|---|
| 上传内容 | 同名**文本文件**，内容 = 链接目标地址 | 链接目标的**真实内容** |
| 是否读目标内容 | 不读 | 读并上传 |
| 断链（目标不存在） | 照常转文本上传 | 跳过并提示 |
| 目录链接 | 只把该链接名转成文本，不展开 | 递归展开目标目录内容（按 realpath 防循环） |
| 任务库记录 | 记入 `task_links`（Rel/Target/ObjectKey/Size） | 不记录 |
| 计入进度/统计 | 计入 total/done/failed | 不计入 |
| 上传失败处理 | 计入失败、中断整体任务 | 仅提示、不中断任务 |
| 下载后还原 | 文本文件自动还原为 symlink | 下载到的就是真实文件，无还原动作 |
| 典型用途 | 归档“链接关系”，不复制大文件 | 自包含拷贝，离开本机后仍完整可用 |

### 什么时候加 `--follow-links`

- 想要**自包含的归档/交付物**：把链接指向的内容一并拷贝上云，异地下载后即可使用，不依赖链接目标仍在本机
- 链接指向的是**项目内/有意义的小文件**，需要它们真正出现在云端
- 需要把**目录链接**的内容整体打包上传

### 什么时候不加（默认）

- 链接指向**外部共享大文件**（如 `/data/share/reference.bam`）：默认转文本可避免把同一份大文件重复上传多份，省空间省流量
- 只想**记录目录结构与链接关系**：下载还原后 symlink 仍指向原路径，方便后续在本地继续工作
- 目录里可能有**断链**：默认模式也能保留“这里曾有个链接、目标地址是 X”的记录

**一句话**：要“链接指向什么”→ 不加；要“链接指向的内容”→ 加 `--follow-links`。

### 示例

```bash
# 默认：sample/mapped.bam 是软链接 → /data/share/big.bam
./aos cp ./sample tos://bucket/ACME/PM-ACME2026001-01/sample
# 云端多一个 mapped.bam（文本，内容为 /data/share/big.bam）+ 任务库记入 task_links

# 加 --follow-links：上传真实内容
./aos cp --follow-links ./sample tos://bucket/ACME/PM-ACME2026001-01/sample
# 云端 mapped.bam 是 /data/share/big.bam 的真实拷贝，不记 task_links
```

**下载侧注意**：`--follow-links` 只在**上传**时生效；下载无论加不加都会把链接文本文件还原为 symlink（前提是当初上传未加 `--follow-links`，即任务库有 `task_links` 记录）。

## 传输行为说明

- **软链接**：默认（不加 `--follow-links`）不读取链接目标内容，转为**同名文本文件**上传（文本内容 = readlink 原值，即链接指向的地址），并把链接明细（相对路径 / 目标地址 / 对象 key）记录到任务数据库 `task_links` 表；下载后自动还原为 symlink（普通下载按 `tos://bucket/前缀` 匹配最近的 up 任务，单参数 `aos cp <本地路径>` 按上传记录还原）；断链（readlink 成功但目标不存在）同样按文本上传。详见「软链接：要不要加 `--follow-links`？」
- **`--follow-links`**：软链接**溯源上传**链接目标的真实内容（key 仍用链接在项目中的相对路径）——目录链接递归展开并按 realpath 防循环、断链跳过并提示；这些溯源文件**不记录到任务数据库**（不计入 total/done/failed 统计）；**溯源链接上传失败仅提示，不中断任务**。详见「软链接：要不要加 `--follow-links`？」
- **路径自动规范化**：`./abc//de/./f` 这类路径会自动规范为 `abc/de/f`，不会产生 `./`、`//`、`..` 段
- 内置默认跳过 `.git/.svn/.DS_Store/.aos/__pycache__/._*/*.checkpoint/*.tmp`（跳过时会有汇总提示）

## cp 选项

```
上传/下载通用:
-j <N>           文件级并发（默认按 CPU 核数，最少 4、最多 16）
-p <N>           单文件分片并发（默认 4）
--part-size <大小> 分片大小（默认 20MB，支持 5MB~5GB，如 20MB 或 5m）
-q               安静模式
--no-record      不写入任务记录数据库
--timeout <时长>  传输总超时（默认 12h，如 30m、2h）

上传:
-e <规则>         排除规则，逗号分隔，支持通配符（*.tmp,.git）；规则若以 - 开头请用 --exclude=规则 形式
--checkpoint      大文件分片上传断点续传（checkpoint 存于上传根目录 .aos/checkpoints/）
--follow-links    软链接溯源上传链接目标内容（这些文件不记录任务）

下载:
-f               忽略下载清单，全部重下
--no-checkpoint  关闭大文件分片下载断点续传（默认开启）

数据库:
--db <路径>       sqlite 数据库路径（默认按 $AOS_DB → $XDG_CONFIG_HOME/aos.db → ~/.config/aos.db 的顺序取）
```

## ls 选项

```
--max-depth <N>   最大显示深度（0 表示不限制，默认全部显示）
-m               显示文件修改时间
```

## rm 选项

```
-r, --recursive   递归删除前缀下所有对象，并顺带清理该前缀下的未完成分片上传任务
-f, --force       跳过批量删除前的确认（非终端环境默认拒绝，需加 -f）
-q, --quiet       安静模式（与 cp 一致）
```

删除语义：

- **单对象删除**（无 `-r`）：直接执行不询问，对象不存在视为删除成功（幂等）；完成后报告「已删除 1 个对象」
- **`-r` 递归删除**：先列出数量并提示「将删除 N 个对象、清理 M 个未完成分片上传任务。确认? (y/N)」，`-f` 跳过
- **整桶**：`aos rm tos://bucket -r` 会列出桶内全部对象；确认提示标明「整个 bucket」，`-f` 仍会先打印警告
- **不逐条打印**被删对象（避免大目录刷屏），完成后报告删除总数与分片清理数
- 单个对象删除失败**不中断**，继续删除其余；对象删除失败或分片 abort 失败均以非零退出码退出（可重跑 `aos rm <路径> -r -f` 继续处理剩余）
- 递归时**顺带 abort** 该前缀下未完成的分片上传任务（断点续传中断残留的孤儿分片，不占可见对象但占用存储）
- `rm` 不写任务数据库（破坏性操作不进 `aos stat` 传输历史）；路径必须是 `tos://` 开头的云上路径
- 开启版本控制的 bucket 上，删除对象仅生成 delete marker，历史版本不会真正删除（S3 语义）

## 断点续传与完整性

- **分片下载**：对 ≥5MB 的对象按分片（默认 20MB）并行 Range 下载，**默认开启断点续传**——中断后重跑会从上次进度继续，checkpoint 文件存于下载目录 `.aos/checkpoints/`（成功完成后 SDK 自动清理；`--no-checkpoint` 关闭）
- **完整性校验**：SDK 客户端默认开启 CRC64 校验，每个分片下载完成、整文件落盘前自动校验，与云端内容指纹不一致会报错（类似 tosutil 的 `-vchecksum`，无需额外参数）
- **CRC 失败自愈**：若断点续传的本地状态损坏（如磁盘故障）导致 CRC64 校验失败，会自动清理残留的 checkpoint/临时文件并无 checkpoint 全量重下一次，避免反复复用损坏状态
- **失败重试**：所有网络请求自动指数退避重试 2 次（100ms/200ms），公网/内网偶发抖动可自动恢复
- 对象级跳过仍由 `.aos/manifest.db`（key + ETag）保证，与分片断点续传互补：manifest 管“哪些对象已完成”，checkpoint 管“单个大文件下到一半”

## 权限要求（重要）

TOS 数据面操作需要账号具备对应权限。若 `check` / `ls` / `cp` 返回 **Access Denied**，请在[火山云控制台](https://console.volcengine.com)为使用的子账号授权，例如：

- IAM 用户绑定 `TOSFullAccess` 策略（或仅授权目标 bucket 的自定义策略）
- 或在 bucket 的桶策略（Bucket Policy）中允许该子账号的 `tos:ListBucket`、`tos:GetObject`、`tos:PutObject` 等操作

## 开发

```bash
go test ./...              # 单元测试（不访问 TOS）
go build -o aos ./cmd/aos  # 编译
make linux                 # 交叉编译 linux/amd64 与 linux/arm64
make docs                  # 用 mkdocs-material 构建 GitHub Pages 站点到 _site/
make serve                 # 本地预览文档（先同步根 README/CHANGELOG 再启动 mkdocs serve）
make build / test / ping
```

说明文档发布在 [seqyuan.github.io/aos](https://seqyuan.github.io/aos/)（仓库需为 Public，或账号具备 GitHub Pages）。站点由 [mkdocs-material](https://squidfunk.github.io/mkdocs-material/) 构建（`mkdocs.yml` + `docs/` 目录）。`docs/index.md`、`docs/changelog.md` 是 `make docs`/`make serve` 时从根 README/CHANGELOG 复制生成的临时页（已 .gitignore，不入库；避免软链在 Windows/CI 下失效），改文档只改根文件即可。本地预览：`make serve`。每次 push `main` 还会把 `_site` 作为 `docs-site` artifact 挂在 [Actions](https://github.com/seqyuan/aos/actions) 上。打 `v*` 标签会触发 Release，附带 Linux 二进制。
