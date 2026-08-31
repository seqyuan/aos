package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seqyuan/annotos/internal/db"
	"github.com/seqyuan/annotos/internal/spi"
	"github.com/seqyuan/annotos/internal/tosx"
)

// cmdCP annotos cp：上传本地目录/文件到 TOS，并记录任务到 sqlite。
func cmdCP(args []string) int {
	fs := flag.NewFlagSet("annotos cp", flag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	contract := fs.String("contract", "", "项目合同号，如 ACME2026001（可省略，从 -spi 推导）")
	spiID := fs.String("spi", "", "SPI 编号，如 PM-ACME2026001-01")
	local := fs.String("d", "", "本地目录或文件（支持相对路径）")
	name := fs.String("name", "", "目标文件夹名（默认取 -d 的 basename）")
	concurrency := fs.Int("concurrency", 0, "并发数（默认按 CPU 核数）")
	checkpoint := fs.Bool("checkpoint", false, "大文件断点续传")
	dryRun := fs.Bool("dry-run", false, "只打印计划，不实际上传")
	exclude := fs.String("exclude", "", "排除规则，逗号分隔，支持通配符，如 *.tmp,.git")
	quiet := fs.Bool("q", false, "安静模式")
	dbPath := fs.String("db", "", "sqlite 数据库路径（默认 ~/.annotos/annotos.db）")
	noRecord := fs.Bool("no-record", false, "不写入任务记录数据库")
	if ok, err := parseFlagSet(fs, args, "用法: annotos cp -contract <合同号> -spi <SPI> -d <本地路径> [选项]\n\n示例:\n  annotos cp -contract ACME2026001 -spi PM-ACME2026001-01 -d /path/project1/matrix\n  annotos cp -spi PM-ACME2026001-01 -d ./matrix   # 自动推导 contract\n  annotos cp -spi PM-ACME2026001-01 -d matrix.zip -name matrix"); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	if *local == "" {
		fmt.Fprintln(os.Stderr, "annotos cp: 缺少 -d 参数（本地路径）")
		fs.Usage()
		return 2
	}
	if *contract == "" && *spiID == "" {
		fmt.Fprintln(os.Stderr, "annotos cp: 必须提供 -contract 或 -spi 参数")
		fs.Usage()
		return 2
	}
	if *spiID != "" {
		if err := spi.ValidateSPI(*spiID); err != nil {
			fmt.Fprintf(os.Stderr, "annotos cp: %v\n", err)
			return 2
		}
	}

	cfg, _, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "annotos cp: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "annotos cp: %v\n（运行 annotos config set 配置凭据）\n", err)
		return 1
	}
	ctx, cancel := newSignalCtx()
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 12*time.Hour) // 上传最长 12 小时
	defer cancel()

	client, err := tosx.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "annotos cp: %v\n", err)
		return 1
	}
	var excludes []string
	if *exclude != "" {
		for _, e := range strings.Split(*exclude, ",") {
			if e = strings.TrimSpace(e); e != "" {
				excludes = append(excludes, e)
			}
		}
	}

	opt := tosx.UploadOptions{
		Contract:    *contract,
		SPI:         *spiID,
		Name:        *name,
		LocalPath:   *local,
		Concurrency: *concurrency,
		Checkpoint:  *checkpoint,
		DryRun:      *dryRun,
		Exclude:     excludes,
		Quiet:       *quiet,
	}

	// sqlite 任务记录
	if !*dryRun && !*noRecord {
		path := *dbPath
		if path == "" {
			if p, err := db.DefaultPath(); err == nil {
				path = p
			}
		}
		rec, err := newUploadRecorder(path, opt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "annotos cp: ⚠️ 无法打开任务数据库 %s: %v\n", path, err)
			fmt.Fprintln(os.Stderr, "annotos cp: 继续上传但本次不记录（可用 -no-record 显式跳过）")
		} else {
			defer rec.close()
			opt.Recorder = rec
		}
	}

	if err := tosx.Upload(ctx, client, cfg, opt, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "annotos cp: %v\n", err)
		return 1
	}
	return 0
}

// uploadRecorder 包装 db.Recorder 并适配 tosx.UploadRecorder。
type uploadRecorder struct {
	database *db.DB
	rec      *db.Recorder
}

func newUploadRecorder(path string, opt tosx.UploadOptions) (*uploadRecorder, error) {
	database, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	// contract 推导在 tosx.Upload 内部完成，这里先推导以便写入
	contract := opt.Contract
	if contract == "" {
		if c, err := spi.ResolveContract(opt.Contract, opt.SPI); err == nil {
			contract = c
		}
	}
	rec, err := db.NewRecorder(database, db.Task{
		SPI:       opt.SPI,
		Contract:  contract,
		LocalPath: filepath.Clean(opt.LocalPath),
		Status:    "running",
	})
	if err != nil {
		database.Close()
		return nil, err
	}
	return &uploadRecorder{database: database, rec: rec}, nil
}

func (u *uploadRecorder) close() {
	if u.database != nil {
		_ = u.database.Close()
	}
}

func (u *uploadRecorder) OnTaskBegin(remotePrefix string, totalFiles int, totalBytes int64, linkCount int) (int64, error) {
	// db.Recorder 在构造时已建任务，这里补充总量信息
	if err := u.database.UpdateTaskMeta(u.rec.TaskID(), remotePrefix, int64(totalFiles), totalBytes, int64(linkCount)); err != nil {
		return u.rec.TaskID(), err
	}
	return u.rec.TaskID(), nil
}

func (u *uploadRecorder) OnLinks(taskID int64, links []tosx.UploadLink) error {
	ls := make([]db.Link, 0, len(links))
	for _, l := range links {
		ls = append(ls, db.Link{LinkRel: l.LocalPath, LinkTarget: l.Target, ObjectKey: l.ObjectKey, Size: l.Size})
	}
	return u.rec.AddLinks(ls)
}

func (u *uploadRecorder) OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error {
	u.rec.Progress(doneFiles, doneBytes, failedFiles)
	return nil
}

func (u *uploadRecorder) OnFinish(taskID int64, status, errMsg string) error {
	return u.rec.Finish(status, errMsg)
}
