package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/seqyuan/aos/internal/db"
	"github.com/seqyuan/aos/internal/spi"
	"github.com/seqyuan/aos/internal/tosx"
)

// cmdDown aos down：下载 TOS 内容到当前目录或指定路径。
// 支持：位置参数 tos 路径 / -spi [-contract] / -id <任务ID> / 仅 -d <cp时路径>。
// 下载完成后若对应任务有软链接记录，自动把文本文件还原为 symlink。
func cmdDown(args []string) int {
	fs := flag.NewFlagSet("aos down", flag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	contract := fs.String("contract", "", "项目合同号（可省略，从 -spi 推导）")
	spiID := fs.String("spi", "", "SPI 编号，如 PM-ACME2026001-01")
	name := fs.String("name", "", "远端文件夹名（省略时自动探测）")
	local := fs.String("d", "", "本地保存目录（支持相对路径，可省略默认 ./<远端文件夹名>）")
	concurrency := fs.Int("concurrency", 0, "并发数（默认按 CPU 核数）")
	overwrite := fs.Bool("overwrite", false, "覆盖本地已存在的同名文件（默认跳过）")
	quiet := fs.Bool("q", false, "安静模式")
	taskID := fs.Int64("id", 0, "按 sqlite 任务记录下载（用 aos stat 查 ID）")
	dbPath := fs.String("db", "", "sqlite 数据库路径")
	noRestore := fs.Bool("no-restore", false, "下载后不自动还原软链接")
	if ok, err := parseFlagSet(fs, args, "用法: aos down [tos路径] [选项]\n\n示例:\n  aos down tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset -d /local\n  aos down -spi PM-ACME2026001-01                # 自动探测远端文件夹，存到 ./dataset/\n  aos down -d /data/project1/dataset                  # 按 cp 时记录的路径回查任务并下载回该路径\n  aos down -id 3 -d /local                            # 按任务 ID 下载\n\n说明: 下载完成后若数据库有对应任务的软链接记录，会自动把下载的文本文件还原为 symlink\n      （内容与记录一致时；可用 -no-restore 关闭）"); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	tosPath := ""
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "aos down: 最多 1 个位置参数（tos 路径）")
		return 2
	}
	if fs.NArg() == 1 {
		tosPath = fs.Arg(0)
	}

	// 需要数据库：-id / 仅 -d 回查；以及自动还原软链接（除非 -no-restore）
	lookupDB := *taskID > 0 || (tosPath == "" && *spiID == "" && *contract == "")
	var database *db.DB
	if lookupDB || !*noRestore {
		path := *dbPath
		if path == "" {
			if p, err := db.DefaultPath(); err == nil {
				path = p
			}
		}
		d, err := db.Open(path)
		if err != nil {
			if lookupDB {
				fmt.Fprintf(os.Stderr, "aos down: 无法打开数据库 %s: %v\n", path, err)
				return 1
			}
			fmt.Fprintf(os.Stderr, "aos down: ⚠️ 无法打开数据库 %s，跳过自动还原: %v\n", path, err)
		} else {
			defer d.Close()
			database = d
		}
	}

	localDirExact := false
	switch {
	case tosPath != "":
		// 位置参数 tos 路径：直接下载
	case *taskID > 0:
		t, err := database.GetTask(*taskID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "任务 %d: %s (%s) 远端=%s\n", t.ID, t.Status, t.SPI, t.RemotePrefix)
		if t.RemotePrefix == "" {
			fmt.Fprintln(os.Stderr, "aos down: 该任务没有记录远端路径（可能未真正上传）")
			return 1
		}
		tosPath = t.RemotePrefix
	case *spiID != "" || *contract != "":
		if *spiID != "" {
			if err := spi.ValidateSPI(*spiID); err != nil {
				fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
				return 2
			}
		}
		// -spi/-contract：由 Download 推导远端
	case *local != "":
		// 只给了 -d：按 cp 时记录的本地路径回查任务，下载回该路径
		t, err := database.FindTaskByLocalPath(*local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
			return 1
		}
		if t.RemotePrefix == "" {
			fmt.Fprintf(os.Stderr, "aos down: 任务 %d（%s）没有记录远端路径\n", t.ID, t.SPI)
			return 1
		}
		fmt.Fprintf(os.Stderr, "找到任务 %d（%s，状态 %s）: %s\n", t.ID, t.SPI, t.Status, t.RemotePrefix)
		tosPath = t.RemotePrefix
		localDirExact = true // 直接还原到 -d 路径本身
	default:
		fmt.Fprintln(os.Stderr, "aos down: 必须提供 tos 路径、-contract/-spi、-id，或 -d（cp 时的本地路径）")
		fs.Usage()
		return 2
	}

	cfg, _, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "aos down: %v\n（运行 aos config set 配置凭据）\n", err)
		return 1
	}
	ctx, cancel := newSignalCtx()
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 12*time.Hour)
	defer cancel()

	client, err := tosx.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
		return 1
	}

	opt := tosx.DownloadOptions{
		Path:          tosPath,
		Contract:      *contract,
		SPI:           *spiID,
		Name:          *name,
		LocalDir:      *local,
		LocalDirExact: localDirExact,
		Concurrency:   *concurrency,
		Overwrite:     *overwrite,
		Quiet:         *quiet,
	}
	// 下载完成后自动还原软链接（需要数据库）
	if database != nil && !*noRestore {
		opt.OnCompleted = func(remotePrefix, localRoot string) {
			restoreLinksFromTask(database, remotePrefix, localRoot, os.Stdout)
		}
	}

	if err := tosx.Download(ctx, client, cfg, opt, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos down: %v\n", err)
		return 1
	}
	return 0
}

// restoreLinksFromTask 按远端前缀找到任务，若其有软链接记录则把下载的文本文件还原为 symlink。
func restoreLinksFromTask(database *db.DB, remotePrefix, localRoot string, w io.Writer) {
	t, err := database.FindTaskByRemotePrefix(remotePrefix)
	if err != nil {
		return // 没有对应任务，不还原
	}
	links, err := database.GetLinks(t.ID)
	if err != nil || len(links) == 0 {
		return
	}
	created, skipped, mismatched := 0, 0, 0
	fmt.Fprintf(w, "自动还原软链接（任务 %d %s，共 %d 条）:\n", t.ID, t.SPI, len(links))
	for _, l := range links {
		dest, err := tosx.SafeJoin(localRoot, l.LinkRel)
		if err != nil {
			fmt.Fprintf(w, "  - 跳过不安全路径 %q: %v\n", l.LinkRel, err)
			skipped++
			continue
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			skipped++
			continue
		}
		if string(data) != l.LinkTarget {
			fmt.Fprintf(w, "  - 内容与记录不一致，跳过 %s\n", dest)
			mismatched++
			continue
		}
		if err := os.Remove(dest); err != nil {
			fmt.Fprintf(w, "  ✗ 无法替换 %s: %v\n", dest, err)
			continue
		}
		if err := os.Symlink(l.LinkTarget, dest); err != nil {
			fmt.Fprintf(w, "  ✗ 创建软链接失败 %s: %v\n", dest, err)
			continue
		}
		fmt.Fprintf(w, "  ✓ %s -> %s\n", dest, l.LinkTarget)
		created++
	}
	fmt.Fprintf(w, "  还原完成：新建 %d，跳过 %d，内容不符 %d\n", created, skipped, mismatched)
}
