# annotos

火山云 TOS 上传 / 下载 / 浏览命令行工具（Go 编写，单二进制分发）。

## 特性

- **`ls`**：以目录树形式列出 TOS 目标路径下的文件（带大小、数量统计）
- **`cp`**：上传本地目录/文件到 `tos://bucket/<contract>/<spi>/<名称>/`，支持并发、排除规则、断点续传、dry-run，并记录任务到 **SQLite**
- **`down`**：从 TOS 下载到本地，支持按 `-d`（cp 时的路径）/`-id`/`-spi` 定位，下载后**自动还原软链接**
- **`stat`**：查询 cp 任务状态（done/break，含文件/字节进度）；`-id` 查看详情与软链接记录
- **`restore`**：单独的软链接还原命令（`down` 已自动执行，此命令用于手动重跑）
- **`check`**：连接与权限诊断（自动尝试公网 endpoint 回退）
- **`config`**：查看 / 配置凭据

## 快速开始

```bash
# 编译
go build -o annotos .

# 查看配置（凭据文件与二进制同目录，随二进制一起拷贝即可使用）
./annotos config

# 查看目录树
./annotos ls tos://example-bucket/ACME2026001
./annotos ls ACME2026001/PM-ACME2026001-01/matrix

# 上传（-contract 可省略，自动从 -spi 推导）
./annotos cp -contract ACME2026001 -spi PM-ACME2026001-01 -d /path/project1/matrix
./annotos cp -spi PM-ACME2026001-01 -d ./matrix

# 下载（支持直接 tos 路径，-d 省略时默认存到 ./<远端文件夹名>）
./annotos down tos://example-bucket/ACME2026001/PM-ACME2026001-01/matrix -d /local/path
./annotos down -spi PM-ACME2026001-01                 # 自动探测远端文件夹
./annotos down -d /data/project1/matrix                    # 按 cp 时的路径回查任务并下载回该路径
./annotos down -id 3 -d /local/path                        # 按任务 ID 下载（annotos stat 查 ID）

# 任务状态
./annotos stat                                             # 最近任务列表
./annotos stat -id 3                                        # 任务详情 + 软链接记录

# down 下载完成后会自动把软链接文本文件还原为 symlink（-no-restore 关闭）；
# 也可手动重跑: annotos restore -id 3 -d /local/matrix
./annotos upload -spi PM-ACME2026001-01 -d matrix.zip -name matrix   # 压缩包传到 matrix 名下

# 下载（支持直接 tos 路径，-d 省略时默认存到 ./<远端文件夹名>）
./annotos download tos://example-bucket/ACME2026001/PM-ACME2026001-01/matrix -d /local/path
./annotos download -spi PM-ACME2026001-01                 # 自动探测远端文件夹，存到 ./matrix/
./annotos download -contract ACME2026001 -spi PM-ACME2026001-01 -name matrix -d /local/path

# 诊断连接与权限
./annotos check
```

## 配置

配置文件 `annotos.json` 位于 **annotos 二进制所在目录**，随二进制一起拷贝即可在任何机器使用：

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
- 修改配置：`./annotos config set -ak AKLT... -sk WXpa... [-endpoint ...] [-bucket ...]`
- 可用环境变量覆盖（便于 CI）：`ANNOTOS_AK` / `ANNOTOS_SK` / `ANNOTOS_ENDPOINT` / `ANNOTOS_REGION` / `ANNOTOS_BUCKET`，或 `ANNOTOS_CONFIG` 指定配置文件路径
- 单次命令覆盖：`-endpoint` / `-region` / `-bucket` 参数

## 路径规则

| 输入 | 解析结果 |
| --- | --- |
| `tos://example-bucket/ACME2026001` | bucket=`example-bucket`, prefix=`ACME2026001/` |
| `example-bucket/ACME2026001` | 首段等于默认 bucket，同上 |
| `ACME2026001/PM-x-07/matrix` | 纯前缀，使用默认 bucket |

## SPI 与 contract

`-contract` 可省略：SPI `PM-ACME2026001-01` → 去掉 `PM-` 前缀、取最后 `-` 之前 → `ACME2026001`。

## 任务记录（SQLite）

- `cp` 会把任务写入 SQLite：时间、SPI/contract、`-d` 路径、远端路径、文件/字节进度、状态（running/done/break）、错误信息
- 软链接记录在独立表 `task_links`（对应主表 `tasks`），记录链接相对路径、readlink 原值、上传后的对象 key
- `down` 下载完成后自动判断：若远端前缀匹配到任务且有软链接记录，把下载回来的文本文件（内容与记录一致时）还原为 symlink；`-no-restore` 可关闭
- 数据库默认 `~/.annotos/annotos.db`，可用 `-db` 参数或环境变量 `ANNOTOS_DB` 指定
- `-dry-run` 不写入记录；`-no-record` 可显式跳过记录
- Ctrl+C / 报错退出时任务标记为 `break`，正常完成标记为 `done`

## 上传行为说明

- **软链接不溯源**：本地软链接（symlink）不会读取链接目标的内容，而是上传一个**同名文本文件**，文件内容写入链接目标地址。例如 `mapped.bam -> /data/share/bigfile.bam` 会上传一个 `mapped.bam` 文本文件，内容为 `/data/share/bigfile.bam`
- **路径自动规范化**：`./abc//de/./f` 这类路径会自动规范为 `abc/de/f`，不会产生 `./`、`//` 段
- 内置默认跳过 `.git/.svn/.DS_Store/.annotos/__pycache__/._*/*.checkpoint/*.tmp`

## upload 选项

```
-contract <合同号>    项目合同号（可省略，从 -spi 推导）
-spi <SPI>            SPI 编号，如 PM-ACME2026001-01
-d <本地路径>          本地目录或文件（支持相对路径）
-name <名称>          目标文件夹名（默认取 -d 的 basename）
-exclude <规则>       排除规则，逗号分隔，支持通配符（*.tmp,.git）
-concurrency <N>      并发数（默认按 CPU 核数）
-checkpoint           大文件断点续传
-dry-run              只打印计划，不实际上传
-q                    安静模式
```

上传目标：`tos://<bucket>/<contract>/<spi>/<名称>/`，本地目录内容保持相对结构。

## 故障排查

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 连接超时 / 卡住 | 本机没有到火山云 VPC 专线（专线网段）的路由 | 换有专线的节点跑（如 192.0.2.1），或改用公网 `-endpoint tos-cn-beijing.volces.com` |
| Access Denied | 桶策略 IP 白名单只放行专线网段 / IAM 未授权 | 先确认在专线节点上是否同样报错；仍报错则在控制台给子账号授权 |
| Credential mal-formed | 配置文件里放了 tosutil 加密后的 ak/sk | annotos.json 必须放**明文** AK/SK（tosutil 配置文件里的值是 AES 加密的，不能直接复用） |

## 权限要求（重要）

TOS 数据面操作需要账号具备对应权限。若 `check` / `ls` / `upload` 返回 **Access Denied**，请在[火山云控制台](https://console.volcengine.com)为使用的子账号授权，例如：

- IAM 用户 `example-bucket` 绑定 `TOSFullAccess` 策略（或仅授权 `example-bucket` bucket 的自定义策略）
- 或在 bucket `example-bucket` 的桶策略（Bucket Policy）中允许该子账号的 `tos:ListBucket`、`tos:GetObject`、`tos:PutObject` 等操作

## 开发

```bash
go test ./...          # 单元测试
go build -o annotos .  # 编译
make build / test / ping   # 或使用 Makefile
```
