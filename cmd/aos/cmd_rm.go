package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/seqyuan/aos/internal/tosx"
	"github.com/spf13/pflag"
)

// cmdRM aos rm：删除对象 / 递归删除前缀 / 清理孤儿分片上传任务。
//
//	aos rm tos://bucket/dir/file.txt       删除单个对象（精确 key，直接删）
//	aos rm tos://bucket/dir -r             递归删除 dir/ 前缀下所有对象 + 清理孤儿分片（需确认）
//	aos rm tos://bucket/dir -r -f          跳过确认
func cmdRM(args []string) int {
	fs := pflag.NewFlagSet("aos rm", pflag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	recursive := fs.BoolP("recursive", "r", false, "递归删除前缀下所有对象，并顺带清理该前缀下的未完成分片上传任务")
	force := fs.BoolP("force", "f", false, "跳过批量删除前的确认")
	quiet := fs.BoolP("quiet", "q", false, "安静模式")
	usage := "用法: aos rm <tos路径> [选项]\n\n删除单个对象（精确 key，直接删）:\n  aos rm tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset.zip\n\n递归删除前缀下所有对象（含孤儿分片清理，需确认）:\n  aos rm tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset -r\n  aos rm tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset -r -f   # 跳过确认\n\n说明:\n  - 单对象删除直接执行、不询问；对象不存在视为删除成功（幂等）\n  - -r 递归删除时先列出数量并询问（y/N），-f 跳过；非终端环境默认拒绝，需加 -f\n  - aos rm tos://bucket -r 会删除整个桶，确认提示标明「整个 bucket」；-f 仍会先打印警告\n  - -r 会顺带 abort 该前缀下未完成的分片上传任务（断点续传中断残留的孤儿分片）\n  - 删除过程不逐条打印，完成后报告删除总数与分片清理数；对象删除或分片 abort 失败均非零退出\n  - 开启版本控制的 bucket 上，删除对象仅生成 delete marker，历史版本不会真正删除（S3 语义）"
	if ok, err := parseFlagSet(fs, args, usage); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "aos rm: 需要 1 个参数（tos 路径）")
		fs.Usage()
		return 2
	}
	if !strings.HasPrefix(fs.Arg(0), "tos://") {
		fmt.Fprintln(os.Stderr, "aos rm: 路径必须是 tos:// 开头的云上路径（如 tos://bucket/dir/file.txt）；rm 不删除本地文件")
		return 2
	}

	cfg, _, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos rm: %v\n", err)
		return 1
	}
	// 云上路径均为显式 tos://（可省略 bucket），连接必需字段为 AK/SK/endpoint
	if err := cfg.ValidateAuth(); err != nil {
		fmt.Fprintf(os.Stderr, "aos rm: %v\n（运行 aos config set 配置凭据）\n", err)
		return 1
	}

	ctx, cancel := newSignalCtx()
	defer cancel()

	client, err := tosx.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos rm: %v\n", err)
		return 1
	}
	if _, err := tosx.RM(ctx, client, cfg, tosx.RMOptions{
		Path:      fs.Arg(0),
		Recursive: *recursive,
		Force:     *force,
		Quiet:     *quiet,
	}, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos rm: %v\n", err)
		return 1
	}
	return 0
}
