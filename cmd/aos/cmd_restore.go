package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seqyuan/aos/internal/db"
	"github.com/seqyuan/aos/internal/tosx"
)

// cmdRestore aos restore：按任务记录还原软链接。
// 对任务中的每条软链接记录，在目标目录下重建同名 symlink 指向记录的 link_target。
func cmdRestore(args []string) int {
	fs := flag.NewFlagSet("aos restore", flag.ContinueOnError)
	taskID := fs.Int64("id", 0, "任务 ID（必填）")
	local := fs.String("d", "", "还原目标目录（默认当前目录）；链接将创建在 {d}/<link_rel>")
	dbPath := fs.String("db", "", "sqlite 数据库路径（默认 ~/.config/aos.db）")
	force := fs.Bool("f", false, "已存在的文件/链接强制覆盖")
	if ok, err := parseFlagSet(fs, args, "用法: aos restore -id <任务ID> [-d <目录>] [-f]\n\n示例:\n  aos restore -id 3 -d /local/dataset    # 在 /local/dataset 下还原任务 3 的软链接"); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	if *taskID <= 0 {
		fmt.Fprintln(os.Stderr, "aos restore: 需要 -id <任务ID>（用 aos stat 查看）")
		fs.Usage()
		return 2
	}

	path := *dbPath
	if path == "" {
		if p, err := db.DefaultPath(); err == nil {
			path = p
		}
	}
	database, err := db.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos restore: 无法打开数据库 %s: %v\n", path, err)
		return 1
	}
	defer database.Close()

	t, err := database.GetTask(*taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos restore: %v\n", err)
		return 1
	}
	links, err := database.GetLinks(*taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos restore: %v\n", err)
		return 1
	}
	if len(links) == 0 {
		fmt.Printf("任务 %d（%s）没有软链接记录，无需还原\n", t.ID, t.SPI)
		return 0
	}

	root := *local
	if root == "" {
		root = "."
	}
	created, skipped, failed := 0, 0, 0
	fmt.Printf("还原任务 %d（%s）的 %d 个软链接到 %s：\n", t.ID, t.SPI, len(links), root)
	for _, l := range links {
		dest, err := tosx.SafeJoin(root, l.LinkRel)
		if err != nil {
			fmt.Printf("  ✗ 不安全路径 %q: %v\n", l.LinkRel, err)
			failed++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fmt.Printf("  ✗ 创建目录失败 %s: %v\n", filepath.Dir(dest), err)
			failed++
			continue
		}
		if _, err := os.Lstat(dest); err == nil {
			if !*force {
				fmt.Printf("  - 已存在，跳过 %s\n", dest)
				skipped++
				continue
			}
			if err := os.Remove(dest); err != nil {
				fmt.Printf("  ✗ 无法替换 %s: %v\n", dest, err)
				failed++
				continue
			}
		}
		if err := os.Symlink(l.LinkTarget, dest); err != nil {
			fmt.Printf("  ✗ 创建软链接失败 %s -> %s: %v\n", dest, l.LinkTarget, err)
			failed++
			continue
		}
		fmt.Printf("  ✓ %s -> %s\n", dest, l.LinkTarget)
		created++
	}
	fmt.Printf("完成：新建 %d，跳过 %d，失败 %d\n", created, skipped, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
