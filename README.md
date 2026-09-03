# aos

对象存储上传 / 下载 / 浏览命令行工具（Go 编写，单二进制分发）。当前后端为火山云 TOS：endpoint / region / bucket / AK/SK 放在配置文件里。传输实现集中在 `internal/tosx`（当前绑定火山云 TOS SDK），未来接入 S3 等其他对象存储时在该层抽取后端接口即可，命令行与配置层无需改动。

## 特性

- **`cp`**：上传 / 下载统一命令，方向由位置参数顺序决定（云上路径带 `tos://` 前缀，与 tosutil 心智一致）
- **`ls`**：以目录树形式列出目标路径下的文件（带大小、数量统计）
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
- 上传/下载任务均写入 SQLite（时间、方向 up/down、本地路径、远端路径、文件/字节进度、状态 running/done/break、错误信息），便于 `aos stat` 查看；`aos cp <本地路径>` 按上传记录还原
- `--no-record` 可显式跳过记录
- Ctrl+C / 报错退出时任务标记为 `break`，正常完成标记为 `done`

## 下载进度（`.aos/manifest.db`）

`cp` 下载时在落盘根目录写入 `.aos/manifest.db`。每个对象下载成功后记录 `object_key` + `etag`（云上内容指纹）。再次下载时：

- 清单里有该 key、ETag 与本次 List 一致、且本地文件还在 → 跳过
- 不在清单、ETag 变了、或本地文件被删 → 重下
- 半截文件不会写入清单（清单只记录完整下载的对象）；单个大文件中途进度由分片 checkpoint 续传（默认开启，见「断点续传与完整性」）
- `-f` 忽略清单，全部重下并更新记录
- 上传默认跳过 `.aos`，不会把清单传回远端

## 传输行为说明

- **软链接**：默认不跟随，遍历时直接跳过（避免误传链接指向的共享大文件或造成循环）；本地路径本身为软链接时默认报错
- **`--follow-links`**：软链接**溯源上传**链接目标的真实内容（key 仍用链接在项目中的相对路径）——目录链接递归展开并按 realpath 防循环、断链跳过并提示；这些溯源文件**不记录到任务数据库**（不计入 total/done/failed 统计）；**溯源链接上传失败仅提示，不中断任务**
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
