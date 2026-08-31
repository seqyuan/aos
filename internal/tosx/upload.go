package tosx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/seqyuan/annotos/internal/config"
	"github.com/seqyuan/annotos/internal/human"
	"github.com/seqyuan/annotos/internal/spi"
	"github.com/seqyuan/annotos/internal/ui"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// UploadOptions upload 命令选项。
type UploadOptions struct {
	Contract    string         // 项目合同号，可为空（从 spi 推导）
	SPI         string         // SPI 编号，如 PM-ACME2026001-01
	Name        string         // 目标文件夹名，默认取 -d 的 basename
	LocalPath   string         // 本地目录或文件，支持相对路径
	Concurrency int            // 并发数
	Checkpoint  bool           // 大文件断点续传
	DryRun      bool           // 只打印不上传
	Exclude     []string       // 排除规则（glob）
	Quiet       bool           // 安静模式
	Recorder    UploadRecorder // 任务记录器（可为 nil）
}

// UploadLink 软链接记录（供 Recorder 持久化，便于后续还原）。
type UploadLink struct {
	LocalPath string // 本地软链接路径（相对上传根目录）
	Target    string // readlink 原值（链接指向的地址）
	ObjectKey string // 上传后的对象 key
	Size      int64  // 文本文件字节数
}

// UploadRecorder 记录 cp 任务：调用方实现（例如写入 SQLite）。
// 返回的 taskID 会在后续回调中原样传回。
type UploadRecorder interface {
	OnTaskBegin(remotePrefix string, totalFiles int, totalBytes int64, linkCount int) (taskID int64, err error)
	OnLinks(taskID int64, links []UploadLink) error
	OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error
	OnFinish(taskID int64, status, errMsg string) error
}

// Upload 执行上传。
func Upload(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt UploadOptions, w io.Writer) error {
	// 1. 推导 contract
	contract, err := resolveContract(opt.Contract, opt.SPI)
	if err != nil {
		return err
	}
	if err := spi.ValidateSPI(opt.SPI); err != nil {
		return err
	}

	// 2. 解析本地路径（支持相对路径，相对当前工作目录）
	localPath := filepath.Clean(opt.LocalPath)
	stat, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("无法访问本地路径 %s: %w", localPath, err)
	}

	// 3. 决定目标名称与 key
	name := opt.Name
	if name == "" {
		name = filepath.Base(localPath)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fmt.Errorf("无法确定目标名称，请用 -name 指定")
	}

	basePrefix := spi.TargetKey(contract, opt.SPI) + "/" + name

	// 4. 收集待上传文件
	var jobs []uploadJob
	if !stat.IsDir() {
		key := normalizeKey(basePrefix)
		if isSymlink(localPath) {
			if target, err := os.Readlink(localPath); err == nil {
				jobs = append(jobs, uploadJob{key: key, linkTarget: target})
			} else {
				return fmt.Errorf("读取软链接 %s 失败: %w", localPath, err)
			}
		} else {
			jobs = append(jobs, uploadJob{local: localPath, key: key})
		}
	} else {
		err := filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if path == localPath {
				return nil
			}
			rel, err := filepath.Rel(localPath, path)
			if err != nil {
				return err
			}
			relSlash := normalizeKey(filepath.ToSlash(rel))
			// 内置默认跳过项 + 用户 -exclude 规则
			if defaultSkip(info.Name(), info.IsDir()) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				// 排除整个目录
				if ExcludeMatch(relSlash, info.Name(), opt.Exclude) {
					return filepath.SkipDir
				}
				return nil
			}
			if ExcludeMatch(relSlash, info.Name(), opt.Exclude) {
				return nil
			}
			key := normalizeKey(basePrefix + "/" + relSlash)
			// 软链接：不溯源，改为上传同名文本文件，内容写入链接目标地址
			if isSymlink(path) {
				target, err := os.Readlink(path)
				if err != nil {
					return fmt.Errorf("读取软链接 %s 失败: %w", path, err)
				}
				jobs = append(jobs, uploadJob{local: path, key: key, linkTarget: target})
				return nil
			}
			jobs = append(jobs, uploadJob{local: path, key: key})
			return nil
		})
		if err != nil {
			return fmt.Errorf("遍历本地目录 %s 失败: %w", localPath, err)
		}
	}

	if len(jobs) == 0 {
		fmt.Fprintf(w, "没有需要上传的文件\n")
		return nil
	}

	// 5. 统计总大小
	var totalBytes int64
	for i := range jobs {
		if jobs[i].linkTarget != "" {
			jobs[i].size = int64(len(jobs[i].linkTarget))
		} else if st, err := os.Stat(jobs[i].local); err == nil {
			jobs[i].size = st.Size()
		}
		totalBytes += jobs[i].size
	}

	// 5.5 任务记录：开始 + 软链接明细
	taskID := int64(0)
	var recorder UploadRecorder = opt.Recorder
	linkCount := 0
	for _, j := range jobs {
		if j.linkTarget != "" {
			linkCount++
		}
	}
	if recorder != nil && !opt.DryRun {
		id, err := recorder.OnTaskBegin(basePrefix, len(jobs), totalBytes, linkCount)
		if err != nil {
			fmt.Fprintf(w, "⚠️ 任务记录失败（继续上传，不记录）: %v\n", err)
			recorder = nil
		} else {
			taskID = id
			links := make([]UploadLink, 0, linkCount)
			for _, j := range jobs {
				if j.linkTarget == "" {
					continue
				}
				rel := strings.TrimPrefix(j.key, basePrefix)
				rel = strings.TrimPrefix(rel, "/")
				links = append(links, UploadLink{
					LocalPath: rel,
					Target:    j.linkTarget,
					ObjectKey: j.key,
					Size:      j.size,
				})
			}
			if err := recorder.OnLinks(taskID, links); err != nil {
				fmt.Fprintf(w, "⚠️ 软链接记录失败: %v\n", err)
			}
		}
	}

	// 6. 打印计划
	extra := ""
	if linkCount > 0 {
		extra = fmt.Sprintf("（含 %d 个软链接转为文本文件）", linkCount)
	}
	fmt.Fprintf(w, "上传 %d 个文件（共 %s）到 tos://%s/%s%s\n",
		len(jobs), human.Size(totalBytes), cfg.Bucket, basePrefix, extra)

	if opt.DryRun {
		for _, j := range jobs {
			if j.linkTarget != "" {
				fmt.Fprintf(w, "  [dry-run] %s -> %s  (软链接: 写入 %q)\n", j.local, j.key, j.linkTarget)
			} else {
				fmt.Fprintf(w, "  [dry-run] %s -> %s\n", j.local, j.key)
			}
		}
		return nil
	}

	// 7. 并发上传
	concurrency := opt.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency()
	}
	progress := ui.NewProgress(len(jobs), totalBytes, opt.Quiet, w)
	progress.Start()

	var wg sync.WaitGroup
	jobCh := make(chan uploadJob)
	var errOnce sync.Once
	var firstErr error
	reportErr := func(e error) {
		errOnce.Do(func() { firstErr = e })
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				if firstErr != nil {
					continue
				}
				var err error
				if j.linkTarget != "" {
					err = UploadText(ctx, client, cfg.Bucket, j.key, j.linkTarget)
				} else {
					err = UploadOne(ctx, client, cfg.Bucket, j.key, j.local, opt.Checkpoint)
				}
				if err != nil {
					reportErr(err)
					progress.Fail(j.key, err)
					if recorder != nil {
						_ = recorder.OnProgress(taskID, 0, 0, 1)
					}
				} else {
					progress.Done(j.key, j.size)
					if recorder != nil {
						_ = recorder.OnProgress(taskID, 1, j.size, 0)
					}
				}
			}
		}()
	}

	// 发送任务
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	progress.Finish()

	// 任务记录：结束
	if recorder != nil {
		status, errMsg := "done", ""
		if firstErr != nil {
			status = "break"
			errMsg = firstErr.Error()
		}
		if err := recorder.OnFinish(taskID, status, errMsg); err != nil {
			fmt.Fprintf(w, "⚠️ 任务状态记录失败: %v\n", err)
		}
	}
	if firstErr != nil {
		return fmt.Errorf("上传失败: %w", firstErr)
	}
	fmt.Fprintf(w, "上传完成 ✅\n")
	return nil
}

type uploadJob struct {
	local      string // 本地文件路径（linkTarget 为空时使用）
	key        string
	size       int64
	linkTarget string // 软链接目标地址；非空时上传同名文本文件、内容为该地址
}

// isSymlink 判断路径是否为软链接（用 Lstat，不跟随）。
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// normalizeKey 规范化 TOS key：去掉空段与 "." 段，折叠连续 "/"。
// 例如 "./abc//de/./f" -> "abc/de/f"。
func normalizeKey(key string) string {
	parts := strings.Split(key, "/")
	out := parts[:0]
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "/")
}

// defaultSkip 内置默认跳过的文件/目录（可被 -exclude 追加，不冲突）。
func defaultSkip(name string, isDir bool) bool {
	switch name {
	case ".git", ".DS_Store", ".annotos", ".svn", "__pycache__", ".ipynb_checkpoints":
		return true
	}
	if strings.HasPrefix(name, "._") {
		return true
	}
	if strings.HasSuffix(name, ".checkpoint") || strings.HasSuffix(name, ".tmp") {
		return true
	}
	return false
}

// resolveContract 从显式 contract 或 spi 推导出 contract。
func resolveContract(contract, spiID string) (string, error) {
	contract = strings.TrimSpace(contract)
	spiID = strings.TrimSpace(spiID)
	if contract != "" {
		return contract, nil
	}
	if spiID == "" {
		return "", fmt.Errorf("必须提供 -contract 或 -spi 参数")
	}
	c, err := spi.ContractFromSPI(spiID)
	if err != nil {
		return "", err
	}
	return c, nil
}

func defaultConcurrency() int {
	n := runtime.NumCPU()
	if n > 16 {
		return 16
	}
	if n < 4 {
		return 4
	}
	return n
}
