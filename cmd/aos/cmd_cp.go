package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seqyuan/aos/internal/db"
	"github.com/seqyuan/aos/internal/tosx"
	"github.com/spf13/pflag"
)

// cmdCP aos cp：上传/下载统一命令，方向由位置参数决定：
//
//	上传:  aos cp <本地路径> tos://<bucket>/<前缀>
//	下载:  aos cp tos://<bucket>/<前缀> <本地目录>
//	还原:  aos cp <本地路径>    （单参数：按上传记录还原下载）
//
// 云上路径必须带 tos://（bucket 可省略为 tos:///前缀，用配置默认桶）。
func cmdCP(args []string) int {
	fs := pflag.NewFlagSet("aos cp", pflag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	// 通用
	job := fs.IntP("job", "j", 0, "文件级并发（默认按 CPU 核数，最少 4、最多 16）")
	partTask := fs.IntP("part-task", "p", 0, "单文件分片并发（默认 4）")
	partSize := fs.String("part-size", "", "分片大小（大文件，默认 20MB，支持 5MB~5GB，如 20MB）")
	quiet := fs.BoolP("quiet", "q", false, "安静模式")
	dbPath := fs.String("db", "", "sqlite 数据库路径（默认 ~/.config/aos.db）")
	timeout := fs.Duration("timeout", 12*time.Hour, "传输总超时（默认 12h，如 30m、2h）")
	// 上传
	exclude := fs.StringP("exclude", "e", "", "排除规则，逗号分隔，支持通配符，如 *.tmp,.git")
	followLinks := fs.Bool("follow-links", false, "软链接溯源上传链接目标的真实内容（这些文件不记录任务数据库）")
	skipExisting := fs.Bool("skip-existing", false, "跳过云端已存在且内容一致的对象（同 key、同大小、同 crc64；适合断线后重跑，省流量）")
	checkpoint := fs.Bool("checkpoint", false, "大文件分片上传断点续传（checkpoint 存于上传根目录 .aos/checkpoints/）")
	noRecord := fs.Bool("no-record", false, "本次传输不写入任务记录数据库（上传/下载均可用）")
	// 下载
	force := fs.BoolP("force", "f", false, "忽略下载清单，全部重下")
	noCheckpoint := fs.Bool("no-checkpoint", false, "关闭大文件分片下载断点续传（默认开启）")
	usage := "用法: aos cp <源> [<目标>] [选项]\n\n上传（本地在前）:\n  aos cp ./dataset tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset\n  aos cp ./dataset tos:///ACME2026001/PM-ACME2026001-01/dataset   # bucket 用配置默认\n  aos cp dataset.zip tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset.zip\n\n下载（tos 在前）:\n  aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset /local\n  aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset   # 单参数：下载到当前目录\n\n单参数本地路径（还原上次上传的数据）:\n  aos cp /data/project1/dataset      # 按上传时记录的路径还原下载回该路径（未上传过会提示）\n\n说明:\n  - 上传目标前缀直接铺入：文件 key = 目标前缀 + 本地相对路径\n  - 目录默认递归；--exclude 排除、--follow-links 溯源软链接、--skip-existing 跳过已存在、--checkpoint 大文件断点续传\n  - 下载按 object key + ETag 清单（.aos/manifest.db）跳过已完成；-f 强制重下\n  - 大文件（≥5MB）分片传输，--part-size 分片大小、-p 单文件分片并发、-j 文件级并发\n  - 上传/下载任务均记录到 sqlite（aos stat 查看历史）；--no-record 可关闭\n  - --timeout 设置传输总超时（默认 12h）；--exclude 排除规则若以 - 开头请用 --exclude=规则 形式"
	if ok, err := parseFlagSet(fs, args, usage); !ok {
		return 2
	} else if err != nil {
		return 2
	}

	pos := fs.Args()
	if len(pos) > 2 {
		fmt.Fprintln(os.Stderr, "aos cp: 最多 2 个位置参数（<源> [<目标>]）")
		return 2
	}
	isTOS := isTOSPath

	// ---- 方向判定 ----
	var src string
	upload, lookupDB := false, false
	// 还原模式（单参数本地路径）：保存查到的 up 任务与其数据库连接，下载完成后按记录还原软链接
	var restoreDB *db.DB
	var restoreTask db.Task
	switch {
	case len(pos) == 2:
		src = pos[0]
		switch {
		case !isTOS(pos[0]) && isTOS(pos[1]):
			upload = true
		case isTOS(pos[0]) && !isTOS(pos[1]):
			// 下载：落到下方下载分支
		case isTOS(pos[0]) && isTOS(pos[1]):
			fmt.Fprintln(os.Stderr, "aos cp: 云端拷贝（tos:// → tos://）暂不支持，将在后续版本提供")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "aos cp: 本地到本地拷贝不支持；云上路径需以 tos:// 开头")
			return 2
		}
	case len(pos) == 1:
		src = pos[0]
		if !isTOS(src) {
			// 单参数本地路径：按上传记录还原下载（未上传过会提示）
			lookupDB = true
		}
		// 单参数 tos:// 路径：下载到当前目录
	default: // 零位置参数
		fmt.Fprintln(os.Stderr, "aos cp: 缺少参数。用法: aos cp <源> [<目标>]（如 aos cp ./dataset tos://bucket/前缀）")
		fs.Usage()
		return 2
	}

	// 分片大小解析
	var partSizeVal int64
	if *partSize != "" {
		v, err := tosx.ParsePartSize(*partSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
			return 2
		}
		partSizeVal = v
	}

	cfg, _, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
		return 1
	}
	// 云上路径均为显式 tos://（可省略 bucket），连接必需字段为 AK/SK/endpoint
	if err := cfg.ValidateAuth(); err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: %v\n（运行 aos config set 配置凭据）\n", err)
		return 1
	}
	ctx, cancel := newSignalCtx()
	defer cancel()
	dur := *timeout
	if dur <= 0 {
		dur = 12 * time.Hour
	}
	ctx, cancel = context.WithTimeout(ctx, dur)
	defer cancel()

	client, err := tosx.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
		return 1
	}

	if upload {
		// 解析目标前缀（bucket 缺省时用配置默认）
		tp, err := tosx.ParseTOSPath(pos[1], cfg.Bucket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
			return 2
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
			Bucket:       tp.Bucket,
			TargetPrefix: tp.Prefix,
			LocalPath:    pos[0],
			Concurrency:  *job,
			Checkpoint:   *checkpoint,
			PartSize:     partSizeVal,
			TaskNum:      *partTask,
			FollowLinks:  *followLinks,
			SkipExisting: *skipExisting,
			Exclude:      excludes,
			Quiet:        *quiet,
		}
		if !*noRecord {
			path := resolveDBPath(*dbPath)
			rec, err := newUploadRecorder(path, opt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "aos cp: 无法打开任务数据库 %s: %v\n", path, err)
				fmt.Fprintln(os.Stderr, "aos cp: 可用 -no-record 显式跳过记录后重试")
				return 1
			}
			defer rec.close()
			opt.Recorder = rec
		}
		if err := tosx.Upload(ctx, client, cfg, opt, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
			return 1
		}
		return 0
	}

	// ---- 下载 ----
	var tosPath, localDir, localFile string
	switch {
	case !lookupDB:
		tosPath = src
		if len(pos) == 2 {
			localDir = pos[1] // 指定本地目录
		}
		// 单参数 tos:// 路径：localDir 为空，Download 落盘到当前目录
	default: // 单参数本地路径：按上传记录还原
		restoreDB, err = openDB(*dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
			return 1
		}
		defer restoreDB.Close()
		restoreTask, err = restoreDB.FindTaskByLocalPath(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
			fmt.Fprintln(os.Stderr, "aos cp: 若想上传请提供目标云上路径，如: aos cp <本地路径> tos://<bucket>/<前缀>")
			return 1
		}
		if restoreTask.RemotePrefix == "" {
			fmt.Fprintf(os.Stderr, "aos cp: 任务 %d 没有记录远端路径\n", restoreTask.ID)
			return 1
		}
		fmt.Fprintf(os.Stderr, "找到上传任务 %d（状态 %s）: %s\n", restoreTask.ID, restoreTask.Status, restoreTask.RemotePrefix)
		tosPath = restoreTask.RemotePrefix
		localDir = src
		localFile = src // 单文件任务时精确落盘回上传时的文件路径
	}

	opt := tosx.DownloadOptions{
		Path:        tosPath,
		LocalDir:    localDir,
		LocalFile:   localFile,
		Concurrency: *job,
		Overwrite:   *force,
		Quiet:       *quiet,
		PartSize:    partSizeVal,
		TaskNum:     *partTask,
		Checkpoint:  !*noCheckpoint,
	}
	// 下载任务记录（-no-record 关闭；与上传共用开关）
	if !*noRecord {
		path := resolveDBPath(*dbPath)
		rec, err := newDownloadRecorder(path, localDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos cp: 无法打开任务数据库 %s: %v\n", path, err)
			fmt.Fprintln(os.Stderr, "aos cp: 可用 -no-record 显式跳过记录后重试")
			return 1
		}
		defer rec.close()
		opt.Recorder = rec
	}
	if err := tosx.Download(ctx, client, cfg, opt, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: %v\n", err)
		return 1
	}
	// 下载完成后还原软链接：
	// 1) 单参数本地路径还原：直接用已查到的 up 任务；
	// 2) 普通下载：按远端前缀匹配最近的 up 任务。
	if err := restoreLinksAfterDownload(*dbPath, restoreDB, restoreTask, lookupDB, tosPath, localDir, cfg.Bucket); err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: 还原软链接失败: %v\n", err)
		return 1
	}
	return 0
}

// restoreLinksAfterDownload 下载完成后按上传记录的软链接明细还原 symlink。
// lookupDB：直接用已查到的 up 任务；普通下载：按远端前缀匹配最近的 up 任务。
func restoreLinksAfterDownload(dbPath string, restoreDB *db.DB, restoreTask db.Task, lookupDB bool, tosPath, localDir, defaultBucket string) error {
	if lookupDB {
		if restoreDB == nil {
			return nil
		}
		return restoreSymlinks(restoreDB, restoreTask.ID, localDir)
	}
	// 普通下载：解析远端前缀（tos://bucket/prefix），匹配最近的 up 任务
	tp, err := tosx.ParseTOSPath(tosPath, defaultBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: 跳过还原软链接（无法解析远端路径）: %v\n", err)
		return nil
	}
	remotePrefix := "tos://" + tp.Bucket + "/" + strings.TrimSuffix(tp.Prefix, "/")
	database, err := openDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: 跳过还原软链接（无法打开任务库）: %v\n", err)
		return nil
	}
	defer database.Close()
	upTask, ok, err := database.FindTaskByRemotePrefix(remotePrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos cp: 跳过还原软链接（查询任务库失败）: %v\n", err)
		return nil
	}
	if !ok {
		return nil // 无匹配 up 任务，跳过还原
	}
	return restoreSymlinks(database, upTask.ID, localDir)
}

func resolveDBPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if p, err := db.DefaultPath(); err == nil {
		return p
	}
	return ""
}

func openDB(flagPath string) (*db.DB, error) {
	return db.Open(resolveDBPath(flagPath))
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
	localStored := filepath.Clean(opt.LocalPath)
	if abs, err := filepath.Abs(opt.LocalPath); err == nil {
		localStored = abs
	}
	rec := db.NewRecorder(database, db.Task{
		Direction: "up",
		LocalPath: localStored,
		Status:    "running",
	})
	return &uploadRecorder{database: database, rec: rec}, nil
}

func (u *uploadRecorder) close() {
	if u.database != nil {
		_ = u.database.Close()
	}
}

func (u *uploadRecorder) OnTaskBegin(remotePrefix string, totalFiles int, totalBytes int64) (int64, error) {
	return u.rec.Begin(remotePrefix, int64(totalFiles), totalBytes)
}

func (u *uploadRecorder) OnLinks(taskID int64, links []tosx.UploadLink) error {
	dl := make([]db.Link, 0, len(links))
	for _, l := range links {
		dl = append(dl, db.Link{Rel: l.Rel, Target: l.Target, ObjectKey: l.ObjectKey, Size: l.Size})
	}
	return u.database.AddLinks(taskID, dl)
}

func (u *uploadRecorder) OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error {
	u.rec.Progress(doneFiles, doneBytes, failedFiles)
	return nil
}

func (u *uploadRecorder) OnFinish(taskID int64, status, errMsg string) error {
	return u.rec.Finish(status, errMsg)
}

// downloadRecorder 记录下载任务到 SQLite（direction=down）。
// remotePath（tos 源路径）在 OnTaskBegin 时写入 remote_prefix。
type downloadRecorder struct {
	database *db.DB
	rec      *db.Recorder
}

func newDownloadRecorder(path, localDir string) (*downloadRecorder, error) {
	database, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	localStored := filepath.Clean(localDir)
	if localStored == "." || localStored == "" {
		if abs, err := os.Getwd(); err == nil {
			localStored = abs
		}
	} else if abs, err := filepath.Abs(localDir); err == nil {
		localStored = abs
	}
	rec := db.NewRecorder(database, db.Task{
		Direction: "down",
		LocalPath: localStored,
		Status:    "running",
	})
	return &downloadRecorder{database: database, rec: rec}, nil
}

func (d *downloadRecorder) close() {
	if d.database != nil {
		_ = d.database.Close()
	}
}

func (d *downloadRecorder) OnTaskBegin(remotePath string, totalFiles int, totalBytes int64) (int64, error) {
	return d.rec.Begin(remotePath, int64(totalFiles), totalBytes)
}

func (d *downloadRecorder) OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error {
	d.rec.Progress(doneFiles, doneBytes, failedFiles)
	return nil
}

func (d *downloadRecorder) OnFinish(taskID int64, status, errMsg string) error {
	return d.rec.Finish(status, errMsg)
}

// restoreSymlinks 下载完成后，按上传时记录的软链接明细把文本文件还原为 symlink。
// localDir 为还原落盘根目录（空则视为当前目录）；顶层链接（Rel 为空）直接在该路径上建链接。
func restoreSymlinks(database *db.DB, taskID int64, localDir string) error {
	if localDir == "" {
		localDir = "."
	}
	links, err := database.GetTaskLinks(taskID)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	for _, l := range links {
		target := localDir
		if l.Rel != "" {
			joined, err := tosx.SafeJoin(localDir, l.Rel)
			if err != nil {
				return fmt.Errorf("软链接路径不安全 %q: %w", l.Rel, err)
			}
			target = joined
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		if err := replaceWithSymlink(target, l.Target); err != nil {
			return fmt.Errorf("还原软链接 %s -> %s: %w", target, l.Target, err)
		}
	}
	fmt.Printf("还原 %d 个软链接（内容为链接目标地址）\n", len(links))
	return nil
}

// isTOSPath 判断是否为云上路径。必须是 tos:// 前缀，拒绝 http://、s3:// 等。
func isTOSPath(s string) bool {
	return strings.HasPrefix(s, "tos://")
}

// replaceWithSymlink 把 path 替换为指向 linkTarget 的 symlink。
// 先在同目录建临时链接再 rename，避免 Remove 成功但 Symlink 失败时丢掉已下载的文本文件。
func replaceWithSymlink(path, linkTarget string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".aos-link-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Remove(tmpName); err != nil {
		return err
	}
	if err := os.Symlink(linkTarget, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
