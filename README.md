# aos

对象存储上传 / 下载 / 浏览命令行工具（Go 编写，单二进制分发）。当前后端为火山云 TOS；endpoint / region / bucket / AK/SK 放在配置文件里，后续接入其他对象存储时改配置即可。

## 特性

- **`ls`**：以目录树形式列出目标路径下的文件（带大小、数量统计）
- **`cp`**（别名 `upload`）：上传本地目录/文件到 `<bucket>/<contract>/<spi>/<名称>/`，支持并发、排除规则、大文件分片 checkpoint，并记录任务到 **SQLite**
- **`down`**（别名 `download` / `dl`）：下载到本地，支持按 `-d`（cp 时的路径）/`-id`/`-spi` 定位，下载后**自动还原软链接**。进度写在下载目录 `.aos/manifest.db`（key + ETag），可用 `-concurrency` 控制并发
- **`stat`**：查询 cp 任务状态（done/break，含文件/字节进度）；`-id` 查看详情与软链接记录
- **`restore`**：单独的软链接还原命令（`down` 已自动执行，此命令用于手动重跑）
- **`check`**：连接与权限诊断（自动尝试公网 endpoint 回退）
- **`config`**：查看 / 配置凭据

## 快速开始

```bash
# 编译
go build -o aos ./cmd/aos

# Linux 二进制也可从 Release 下载：
# https://github.com/seqyuan/aos/releases
# 在线说明：https://seqyuan.github.io/aos/

# 查看配置（凭据文件与二进制同目录，随二进制一起拷贝即可使用）
./aos config

# 查看目录树
./aos ls tos://example-bucket/ACME2026001
./aos ls ACME2026001/PM-ACME2026001-01/dataset

# 上传（-contract 可省略，自动从 -spi 推导）
./aos cp -contract ACME2026001 -spi PM-ACME2026001-01 -d /path/project1/dataset
./aos cp -spi PM-ACME2026001-01 -d ./dataset

# 下载（支持直接 tos 路径，-d 省略时默认存到 ./<远端文件夹名>）
./aos down tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset -d /local/path
./aos down -spi PM-ACME2026001-01                 # 自动探测远端文件夹
./aos down -d /data/project1/dataset              # 按 cp 时的路径回查任务并下载回该路径
./aos down -id 3 -d /local/path                   # 按任务 ID 下载（aos stat 查 ID）

# 任务状态
./aos stat                                        # 最近任务列表
./aos stat -id 3                                  # 任务详情 + 软链接记录

# down 下载完成后会自动把软链接文本文件还原为 symlink（-no-restore 关闭）；
# 也可手动重跑: aos restore -id 3 -d /local/dataset
./aos cp -spi PM-ACME2026001-01 -d dataset.zip -name dataset   # 压缩包传到 dataset 名下

# 诊断连接与权限
./aos check
```

示例中的合同号 `ACME2026001`、子项目编号 `PM-ACME2026001-01`、bucket `example-bucket` 均为虚构，请换成实际值。

## 配置

配置文件 `aos.json` 位于 **aos 二进制所在目录**，随二进制一起拷贝即可在任何机器使用：

```json
{
  "endpoint": "tos-cn-beijing.ivolces.com",
  "region": "cn-beijing",
  "bucket": "example-bucket",
  "access_key_id": "AKLT...",
  "secret_access_key": "WXpa..."
}
```

- **endpoint 说明**：
  - 内网 / 专线环境：`tos-cn-beijing.ivolces.com`（走火山云 VPC 内网，公网不可达）
  - 公网环境：`tos-cn-beijing.volces.com`
- 修改配置：`./aos config set -ak AKLT... -sk WXpa... [-endpoint ...] [-bucket ...]`
- 可用环境变量覆盖（便于 CI）：`AOS_AK` / `AOS_SK` / `AOS_ENDPOINT` / `AOS_REGION` / `AOS_BUCKET`，或 `AOS_CONFIG` 指定配置文件路径
- 单次命令覆盖：`-endpoint` / `-region` / `-bucket` 参数

## 路径规则

| 输入 | 解析结果 |
| --- | --- |
| `tos://example-bucket/ACME2026001` | bucket=`example-bucket`, prefix=`ACME2026001/` |
| `example-bucket/ACME2026001` | 首段等于默认 bucket，同上 |
| `ACME2026001/PM-ACME2026001-01/dataset` | 纯前缀，使用默认 bucket |

## SPI 与 contract

`-spi` 必填，`-contract` 可省略：SPI `PM-ACME2026001-01` → 去掉 `PM-` 前缀、取最后 `-` 之前 → `ACME2026001`。

## 任务记录（SQLite）

- `cp` 会把任务写入 SQLite：时间、SPI/contract、`-d` 路径、远端路径、文件/字节进度、状态（running/done/break）、错误信息
- 软链接记录在独立表 `task_links`（对应主表 `tasks`），记录链接相对路径、readlink 原值、上传后的对象 key
- `down` 下载完成后自动判断：若远端前缀匹配到任务且有软链接记录，把下载回来的文本文件（内容与记录一致时）还原为 symlink；`-no-restore` 可关闭
- 数据库默认 `~/.config/aos.db`（或 `$XDG_CONFIG_HOME/aos.db`），可用 `-db` 参数或环境变量 `AOS_DB` 指定
- `-dry-run` 不写入记录；`-no-record` 可显式跳过记录
- Ctrl+C / 报错退出时任务标记为 `break`，正常完成标记为 `done`

## 下载进度（`.aos/manifest.db`）

`down` 在落盘根目录写入 `.aos/manifest.db`。每个对象下载成功后记录 `object_key` + `etag`（云上内容指纹）。再次 `down` 时：

- 清单里有该 key、ETag 与本次 List 一致、且本地文件（或已还原的软链接）还在 → 跳过
- 不在清单、ETag 变了、或本地文件被删 → 重下
- 半截文件不会写入清单，下次整文件重下（不做分片 checkpoint）
- `-overwrite` 忽略清单，全部重下并更新记录
- `cp` 默认跳过 `.aos`，不会把清单传回远端

## 上传行为说明

- **软链接不溯源**：本地软链接（symlink，含指向目录的顶层链接）不会读取链接目标的内容，而是上传一个**同名文本文件**，文件内容写入链接目标地址。例如 `mapped.bam -> /data/share/bigfile.bam` 会上传一个 `mapped.bam` 文本文件，内容为 `/data/share/bigfile.bam`
- **路径自动规范化**：`./abc//de/./f` 这类路径会自动规范为 `abc/de/f`，不会产生 `./`、`//`、`..` 段
- 内置默认跳过 `.git/.svn/.DS_Store/.aos/__pycache__/._*/*.checkpoint/*.tmp`

## cp 选项

```
-contract <合同号>    项目合同号（可省略，从 -spi 推导）
-spi <SPI>            SPI 编号，如 PM-ACME2026001-01（必填）
-d <本地路径>          本地目录或文件（支持相对路径）
-name <名称>          目标文件夹名（默认取 -d 的 basename）
-exclude <规则>       排除规则，逗号分隔，支持通配符（*.tmp,.git）
-concurrency <N>      并发数（默认按 CPU 核数，最少 4、最多 16）
-checkpoint           大文件分片上传时启用 SDK checkpoint
-dry-run              只打印计划，不实际上传
-q                    安静模式
```

上传目标：`tos://<bucket>/<contract>/<spi>/<名称>/`，本地目录内容保持相对结构。

`-checkpoint` 只作用于超过 5MB 的单个大文件分片上传；重新执行 `cp` 仍会遍历并上传清单中的每个文件。

## down 选项

```
-concurrency <N>      并发数（默认按 CPU 核数，最少 4、最多 16）
-overwrite            忽略清单，全部重下
-no-restore           下载后不自动还原软链接
```

## 权限要求（重要）

TOS 数据面操作需要账号具备对应权限。若 `check` / `ls` / `cp` 返回 **Access Denied**，请在[火山云控制台](https://console.volcengine.com)为使用的子账号授权，例如：

- IAM 用户绑定 `TOSFullAccess` 策略（或仅授权目标 bucket 的自定义策略）
- 或在 bucket 的桶策略（Bucket Policy）中允许该子账号的 `tos:ListBucket`、`tos:GetObject`、`tos:PutObject` 等操作

## 开发

```bash
go test ./...          # 单元测试（不访问 TOS）
go build -o aos ./cmd/aos  # 编译
make linux                 # 交叉编译 linux/amd64 与 linux/arm64
make docs                  # 从 README 生成 GitHub Pages 站点到 _site/
make build / test / ping
```

说明文档发布在 [seqyuan.github.io/aos](https://seqyuan.github.io/aos/)。打 `v*` 标签会触发 Release，附带 Linux 二进制。
