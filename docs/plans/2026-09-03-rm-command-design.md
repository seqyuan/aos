# aos rm 命令设计

日期：2026-09-03
状态：已确认（待实现）

## 目标

为 aos 新增 `rm` 命令，用于删除 TOS bucket 上的对象。参考火山云 tosutil `rm`（删除桶/对象/分片上传任务），结合 aos「项目数据搬运」定位，删除对象与孤儿分片，**不做删桶**。

范围（已与用户确认）：A 单对象删除 + B `-r` 前缀递归删除 + C 递归时顺带清理该前缀未完成分片上传任务；不做删桶。

## 已确认的设计决策

| # | 决策点 | 结论 |
|---|---|---|
| Q1 | 删除对象范围 | A（单对象）+ B（前缀递归）+ C（分片清理）；不做删桶 |
| Q2 | 确认机制 | 折中型：单对象直接删；`-r` 递归删除时提示数量 + y/N 确认；`-f` 跳过 |
| Q2+ | 输出 | **不逐条打印**被删对象（避免大目录刷屏）；删除后报告**文件总数** |
| Q3 | 命令形态 | 对齐 ossutil/tosutil：默认删**精确对象**；加 `-r` 才递归删除前缀下所有 |
| Q4 | 分片清理方式 | 合并执行：`-r` 递归删除时顺带 abort 该前缀下未完成分片上传任务 |
| Q5 | 失败处理 | 幂等 + 容错：单对象不存在不报错；批量中单个失败不中断，继续删除其余，最后汇总失败数并非零退出 |

## 命令语法

```
用法: aos rm <tos路径> [选项]

删除单个对象（精确 key，直接删，无需确认）:
  aos rm tos://bucket/dir/file.txt

递归删除前缀下所有对象（含孤儿分片清理，需确认）:
  aos rm tos://bucket/dir -r
  aos rm tos://bucket/dir -r -f          # 跳过确认

选项:
  -r, --recursive   删除该前缀下所有对象，并顺带 abort 该前缀下的未完成分片上传任务
  -f, --force       跳过批量删除前的确认
  -q, --quiet       安静模式（与 cp 一致）
```

多字符选项走 `--`（`--recursive`/`--force`/`--quiet`），与 pflag 迁移后约定一致；`-r`/`-f`/`-q` 为单字符 shorthand。

## 行为细节

1. **单对象删除**：路径解析为精确 key → `DeleteObjectV2`，直接执行不询问。幂等（不存在视为成功）。完成后报告「已删除 1 个对象」。
2. **递归删除（`-r`）**：`ListAll` 列出前缀下对象 → 提示「将删除 N 个对象，并清理 M 个未完成分片上传任务。确认? (y/N)」，读终端输入；`-f` 跳过。
3. **删除过程不逐条打印**；单个对象删除失败不中断，继续处理其余。
4. **完成报告**：「已删除 X 个对象（失败 Y 个），清理 Z 个分片上传任务」；有失败时非零退出码，失败详情直接打印（rm 不写任务数据库——破坏性操作不进传输历史）。
5. 与 cp/ls 一致：路径必须带 `tos://`，支持 `tos:///前缀`（配置默认桶）；目录占位对象（key 以 `/` 结尾、size 0）跳过不删。
6. 确认交互仅终端（TTY）下询问；非 TTY/管道下默认拒绝并提示加 `-f`（防脚本误删）。

## 技术实现

### 架构

```
cmd/aos/
  cmd_rm.go                 # CLI 层：flag 解析、确认交互、退出码
  main.go                   # run() 注册 "rm"（+ usage 补充）
internal/tosx/
  rm.go                     # 传输层：RM 核心逻辑
```

### 关键实现点

- **模式判定**（复用 `ParseTOSPath`）：
  - 无 `-r`：prefix 去尾斜杠作精确 key → `DeleteObjectV2`
  - 有 `-r`：prefix → `ListAll` 收集 → 过滤目录占位对象 → 分页批量删
- **批量删除**：`DeleteMultiObjects`（上限 1000/次，与 ListAll 页大小一致），`Quiet: true`；失败项从 `Output.Error` 收集，不中断。
- **分片清理**（`-r` 时）：`ListMultipartUploadsV2` 列该前缀未完成任务 → 逐个 `AbortMultipartUpload`，失败不中断只计数。
- **确认注入**：`RMOptions.Confirm func(prompt string)(bool,error)` 便于单测。

```go
type RMOptions struct {
    Path      string                          // tos://bucket/prefix 或 tos://bucket/key
    Recursive bool                            // -r
    Force     bool                            // -f
    Quiet     bool                            // -q
    Confirm   func(prompt string) (bool, error) // 可注入，默认读终端
}
func RM(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt RMOptions, w io.Writer) error
```

### SDK 接口（ve-tos-golang-sdk v2 已确认可用）

- `DeleteObjectV2(ctx, *DeleteObjectV2Input)` — 单对象删除
- `DeleteMultiObjects(ctx, *DeleteMultiObjectsInput)` — 批量删除（Objects []ObjectTobeDeleted，≤1000）
- `ListMultipartUploadsV2(...)` — 列未完成分片上传
- `AbortMultipartUpload(ctx, *AbortMultipartUploadInput)` — 中止分片上传

## 测试计划（不访问真实 TOS，沿用注入模式）

1. **路径解析**：`tos://b/key`（单对象精确 key）、`tos://b/dir/`（目录）、`tos:///prefix`（默认桶）
2. **确认逻辑**：注入 Confirm true/false → 执行/中止；`-f` 跳过
3. **幂等**：注入 delete 返回 404 → 不报错
4. **批量失败不中断**：注入部分失败 → 继续下一批、最后报失败数
5. **分片清理**：注入 ListMultipartUploads 返回若干任务 → 逐个 abort 计数
6. **CLI 层**：非法参数退出码 2、缺路径报错提示、`-r` 与无 `-r` 的行为差异
