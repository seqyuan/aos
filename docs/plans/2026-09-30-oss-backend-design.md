# aos 多后端（TOS / OSS）设计

## 背景

`aos` 当前把火山云 TOS SDK 直接焊进 `internal/tosx`：所有编排层函数签名都带
`*tos.ClientV2`，列表结果用 `tos.ListedObjectV2`，删除用 `tos.ObjectTobeDeleted` 等。
用户仅修改 `aos.json` 指向阿里云 OSS endpoint 无法工作——TOS SDK 用
`TOS4-HMAC-SHA256` 与 `x-tos-*` 头签名，OSS 用 `OSS4-HMAC-SHA256`/`OSS <ak>:<sig>`
与 `x-oss-*`，二者互不识别。

## 目标

1. 抽象对象存储后端，接入阿里云 OSS；TOS 行为不回退。
2. 后端选择：显式 `provider` 优先，缺省按 endpoint 自动识别。
3. 云路径 scheme：`tos://` / `oss://` 必须与配置后端一致（不一致报错），`s3://` 为中性别名。
4. 新增 `-c` 作为 `--config` 短选项；默认仍是二进制同目录 `aos.json`。

## 非目标

- 云上到云上拷贝（`cp 云→云`）仍不支持。
- 删除 bucket 本身。
- 通用 S3 后端（MinIO 等）：接口已预留，本次只实现 TOS + OSS。

## 架构

```
cmd/aos  ─────────────►  internal/objstore
                          ├── Backend 接口（跨后端统一）
                          ├── tosBackend（火山 TOS SDK）
                          ├── ossBackend（阿里云 OSS SDK）
                          ├── 编排：Upload / Download / LS / RM
                          └── 路径解析 ParseCloudPath / SafeJoin
```

### 后端接口

```go
type Object struct { Key string; Size int64; ETag string; CRC64 uint64; LastModified time.Time }
type Fingerprint struct { Size int64; CRC64 uint64 }
type MultipartUpload struct { Key, UploadID string }
type TransferOpt struct { PartSize int64; TaskNum int; CheckpointDir string }

type Backend interface {
    // 唯一的列举/指纹原语：ListAll 基于 ListPage 实现；
    // Fingerprints 由后端自行选最优路径（TOS 取自 List；OSS List + 并发 HEAD）
    ListPage(ctx, bucket, prefix string, maxKeys int, token string) (objs []Object, nextToken string, err error)
    Fingerprints(ctx, bucket, prefix string, keys []string) (map[string]Fingerprint, error)
    PutFile(ctx, bucket, key, localPath string, opt TransferOpt) error
    PutBytes(ctx, bucket, key string, data []byte) error
    GetFile(ctx, bucket, key, localPath string, size int64, opt TransferOpt) error
    DeleteObject(ctx, bucket, key string) error
    DeleteObjects(ctx, bucket string, keys []string) (failed []string, err error)
    ListUploads(ctx, bucket, prefix string) ([]MultipartUpload, error)
    AbortUpload(ctx, bucket, key, uploadID string) error
}

// 包内自由函数：分页拉全量
func ListAll(ctx, be Backend, bucket, prefix string) ([]Object, error)
```

设计要点：接口只保留 `ListPage`（列举）与 `Fingerprints`（内容指纹）两个
“能力原语”，后端差异（TOS List 自带 crc64、OSS List 没有 crc64）被封装在
各自实现里，编排层无感知。`Provider()` / `Stat()` 等仅测试用或与编排职责重叠
的方法已移除。

工厂：`NewClient(cfg config.Config) (Backend, error)` 按 `cfg.ProviderOrDefault()` 分派。

### 配置

`aos.json` 新增可选字段 `provider`（`tos` | `oss`）。

- `ProviderOrDefault()`：显式优先；否则 endpoint 含 `aliyuncs.com` → oss；
  含 `volces.com`/`ivolces.com` → tos；否则 tos。
- `EndpointOrDefault()`：oss 且 endpoint 为空时按 `oss-<region>.aliyuncs.com` 推导。
- `Scheme()`：返回后端名，用于路径显示与任务库远端前缀（`tos://` / `oss://`）。
- `ValidateAuth()`：oss 不要求 region；endpoint 与 region 不可同时为空。

### 后端差异

| 能力 | TOS | OSS |
| --- | --- | --- |
| List 携带 CRC64 | 是（`HashCrc64ecma`） | 否（List 无 CRC64） |
| CRC64 算法 | `crc64.ECMA` | `crc64.ECMA`（同） |
| 指纹补充 | 无需 | `--skip-existing` 命中同 key 时 `Stat`(HEAD `x-oss-hash-crc64ecma`) 补 |
| 断点续传 | SDK `CheckpointFile` | `oss.CheckpointDir` |
| 分片并发 | `TaskNum` | `oss.Routines` |
| 错误 | 文本匹配 | `oss.ServiceError.Code` |

`--skip-existing` 在 OSS 下对「同 key 同 size」的候选做一次 HEAD 取 CRC64；
HEAD 失败则保守上传（不误跳过）。

## 命令行

- `-c, --config`：配置文件路径（默认二进制同目录 `aos.json`）。
- 云路径 `IsCloudPath`：接受 `tos://`、`oss://`、`s3://`；`ValidateScheme` 校验显式 scheme 与配置后端一致（`s3://` 中性）。
- 同机用多后端：各一份 `aos.json`，用 `-c` 切换（或二进制多副本 + 同目录配置）。
- usage / 帮助文案提示 OSS 与 `provider`。

## 测试

- 编排层（Upload/Download/LS/RM）用内存 fake `Backend` 离线端到端覆盖
  （上传下载往返、`--skip-existing`、清单跳过）。
- `ParseCloudPath` / `IsCloudPath` / `ValidateScheme` 覆盖三种 scheme 与一致性校验。
- `ProviderOrDefault`、`EndpointOrDefault` 覆盖显式 / 自动识别 / provider 感知缺省。
- `filterExistingJobs` 覆盖内容一致/不一致/无 crc64 保守上传。
- TOS/OSS 真实连通性由 `aos check` 人工验证；OSS 的 CRC64 一致性需真机校验。

## 迁移

包 `internal/tosx` 重命名为 `internal/objstore`，类型 `TOSPath` → `CloudPath`。
数据库 `remote_prefix` 字段语义不变（仍是 `<scheme>://bucket/prefix`）。
