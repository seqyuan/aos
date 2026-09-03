package tosx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/seqyuan/aos/internal/config"
	"github.com/seqyuan/aos/internal/human"
	"github.com/seqyuan/aos/internal/manifest"
	"github.com/seqyuan/aos/internal/ui"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// DownloadOptions 下载选项（aos cp <tos源> <本地目录>）。
// 源前缀下的对象按相对路径直接铺入本地目录，不再套一层目录名。
type DownloadOptions struct {
	Path        string // TOS 源路径（tos://bucket/prefix，或 tos:///prefix 用默认 bucket）
	LocalDir    string // 本地落盘目录（支持相对路径；空为当前目录）
	LocalFile   string // 单文件模式下的精确落盘路径（还原下载时使用，目录模式忽略）
	Concurrency int
	Overwrite   bool // 忽略清单，全部重下（与 README 一致；本地存在但未入清单仍会覆盖）
	Quiet       bool
	PartSize    int64            // 分片大小（0 用默认 20MB）
	TaskNum     int              // 单文件分片并发（0 用默认 4）
	Checkpoint  bool             // 大文件分片下载断点续传（默认开启，checkpoint 存于 .aos/checkpoints/）
	Recorder    DownloadRecorder // 任务记录器（可为 nil）
}

// DownloadRecorder 记录下载任务：调用方实现（例如写入 SQLite）。
// 与 UploadRecorder 同构，返回的 taskID 会在后续回调中原样传回。
type DownloadRecorder interface {
	OnTaskBegin(remotePath string, totalFiles int, totalBytes int64) (taskID int64, err error)
	OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error
	OnFinish(taskID int64, status, errMsg string) error
}

// pathSource 是 tos:// 路径解析后、真正用于 List/Get 的位置。
type pathSource struct {
	Bucket string
	Prefix string
}

// resolvePathSource 从用户路径得到 bucket/prefix。
// 显式 tos://other-bucket/... 必须使用路径中的 bucket，不能回落到配置默认值。
func resolvePathSource(path, defaultBucket string) (pathSource, error) {
	tp, err := ParseTOSPath(path, defaultBucket)
	if err != nil {
		return pathSource{}, err
	}
	return pathSource{Bucket: tp.Bucket, Prefix: tp.Prefix}, nil
}

// Download 执行下载：把 tos:// 源前缀下的对象按相对路径下载到本地目录。
func Download(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt DownloadOptions, w io.Writer) error {
	if opt.Path == "" {
		return fmt.Errorf("缺少 tos:// 源路径")
	}
	src, err := resolvePathSource(opt.Path, cfg.Bucket)
	if err != nil {
		return err
	}
	bucket := src.Bucket
	remotePrefix := src.Prefix

	files, remotePrefix, isSingleFile, err := resolveDownloadSource(ctx,
		func(c context.Context, b, p string) ([]tos.ListedObjectV2, error) {
			return ListAll(c, client, b, p)
		}, bucket, remotePrefix)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintf(w, "远端 tos://%s/%s 下没有文件\n", bucket, strings.TrimSuffix(remotePrefix, "/"))
		return nil
	}

	// 本地目标根目录：前缀直接铺入（LocalDir 为空时即当前目录）
	localRoot := opt.LocalDir
	if localRoot == "" {
		localRoot = "."
	}
	// 单文件精确还原（-d 回查 up 文件路径）：manifest/checkpoint 与文件同级目录
	manRoot := localRoot
	if isSingleFile && opt.LocalFile != "" {
		manRoot = filepath.Dir(opt.LocalFile)
	}

	man, err := manifest.Open(manRoot)
	if err != nil {
		return fmt.Errorf("打开下载清单失败: %w", err)
	}
	defer man.Close()
	done, err := man.Completed()
	if err != nil {
		return fmt.Errorf("读取下载清单失败: %w", err)
	}

	// 下载断点续传：大文件分片下载的 checkpoint 文件集中存于 .aos/checkpoints/
	// （与 manifest 同级），成功后 SDK 自动清理；中断残留则下次可续传。
	checkpointDir := ""
	if opt.Checkpoint {
		checkpointDir = filepath.Join(manRoot, ".aos", "checkpoints")
		if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
			checkpointDir = "" // 无法创建目录时退化为不带 checkpoint
		}
	}
	var totalBytes int64
	type dlJob struct {
		key  string
		dest string
		rel  string
		etag string
		size int64
	}
	var jobs []dlJob
	for _, o := range files {
		rel := strings.TrimPrefix(o.Key, remotePrefix)
		if rel == "" {
			rel = filepath.Base(strings.TrimSuffix(o.Key, "/"))
		}
		dest := ""
		if isSingleFile && opt.LocalFile != "" {
			// -d 回查上传时的文件路径：落盘回原始文件路径
			dest = opt.LocalFile
		} else {
			var err error
			dest, err = SafeJoin(localRoot, rel)
			if err != nil {
				return fmt.Errorf("对象 key 不安全 %s: %w", o.Key, err)
			}
		}
		jobs = append(jobs, dlJob{key: o.Key, dest: dest, rel: rel, etag: o.ETag, size: o.Size})
		totalBytes += o.Size
	}

	fmt.Fprintf(w, "下载 %d 个文件（共 %s）到 %s\n", len(jobs), human.Size(totalBytes), localRoot)

	toDo := jobs[:0]
	cachedSkip := 0 // 清单 ETag 未变而跳过的已完成文件数
	var skipBytes int64
	for _, j := range jobs {
		if skipCompleted(opt.Overwrite, done[j.key], j.etag, j.dest) {
			cachedSkip++
			skipBytes += j.size
			continue
		}
		toDo = append(toDo, j)
	}
	if len(toDo) == 0 {
		fmt.Fprintf(w, "所有文件已在清单中，无需下载 ✅（跳过 %d 个已完成文件，共 %s；-f 可强制重下）\n", cachedSkip, human.Size(skipBytes))
		return nil
	}

	concurrency := opt.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency()
	}
	progress := ui.NewProgress(len(toDo), totalBytes-skipBytes, opt.Quiet, w)
	progress.Start()

	// 任务记录：开始（只统计实际待下载的文件）
	taskID := int64(0)
	var recorder DownloadRecorder = opt.Recorder
	if recorder != nil {
		id, err := recorder.OnTaskBegin(opt.Path, len(toDo), totalBytes-skipBytes)
		if err != nil {
			fmt.Fprintf(w, "⚠️ 任务记录失败（继续下载，不记录）: %v\n", err)
			recorder = nil
		} else {
			taskID = id
		}
	}

	var wg sync.WaitGroup
	jobCh := make(chan dlJob)
	var mu sync.Mutex
	var firstErr error
	reportErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	cancelled := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}
	// 因前序失败而未执行的文件数（用于总结提示）
	var skipMu sync.Mutex
	skippedCount := 0
	countSkip := func() {
		skipMu.Lock()
		skippedCount++
		skipMu.Unlock()
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				if cancelled() {
					// 前序文件已失败：剩余任务不执行（未执行不计入失败数，仅提示跳过）
					progress.Skip(j.key)
					countSkip()
					continue
				}
				if err := os.MkdirAll(filepath.Dir(j.dest), 0o755); err != nil {
					reportErr(err)
					progress.Fail(j.key, err)
					if recorder != nil {
						_ = recorder.OnProgress(taskID, 0, 0, 1)
					}
					continue
				}
				if err := DownloadOne(ctx, client, bucket, j.key, j.dest, j.size, opt.PartSize, opt.TaskNum, checkpointDir); err != nil {
					reportErr(err)
					progress.Fail(j.key, err)
					if recorder != nil {
						_ = recorder.OnProgress(taskID, 0, 0, 1)
					}
				} else {
					if err := man.MarkDone(manifest.Object{Key: j.key, ETag: j.etag, Rel: j.rel, Size: j.size}); err != nil {
						// 文件已成功下载，仅清单更新失败：提示但不中断任务。
						// 下次下载时该 key 不在清单中，会按 ETag 校验重新下载。
						fmt.Fprintf(w, "  ⚠️ 下载清单更新失败（文件已下载，下次将重下）: %v\n", err)
					}
					progress.Done(j.key, j.size)
					if recorder != nil {
						_ = recorder.OnProgress(taskID, 1, j.size, 0)
					}
				}
			}
		}()
	}
	for _, j := range toDo {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	progress.Finish()

	if skippedCount > 0 {
		fmt.Fprintf(w, "其余 %d 个文件已跳过（因前序失败，未执行）\n", skippedCount)
	}
	if cachedSkip > 0 && !opt.Quiet {
		fmt.Fprintf(w, "跳过 %d 个已完成文件（共 %s，清单 .aos/manifest.db 中 ETag 未变；-f 可强制重下）\n", cachedSkip, human.Size(skipBytes))
	}

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
		return fmt.Errorf("下载失败: %w", firstErr)
	}
	fmt.Fprintf(w, "下载完成 ✅\n")
	return nil
}

// skipCompleted 清单中有相同 ETag 且本地目标仍存在时跳过。不比较文件大小。
func skipCompleted(overwrite bool, recordedETag, remoteETag, dest string) bool {
	if overwrite {
		return false
	}
	if recordedETag == "" || recordedETag != manifest.NormalizeETag(remoteETag) {
		return false
	}
	_, err := os.Lstat(dest)
	return err == nil
}

// resolveDownloadSource 确定实际要下载的远端文件列表。
// 优先按目录前缀（带尾斜杠）列出；目录下没有文件时回退到“精确对象 key”模式，
// 以支持 up 单文件或顶层软链接上传（对象 key 不带尾斜杠，前缀 + "/" 永远列不到）。
// 返回：文件列表、实际使用的前缀、是否单文件模式。
func resolveDownloadSource(ctx context.Context, list func(ctx context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error), bucket, remotePrefix string) ([]tos.ListedObjectV2, string, bool, error) {
	objs, err := list(ctx, bucket, remotePrefix)
	if err != nil {
		return nil, "", false, err
	}
	files := collectFiles(objs)
	if len(files) > 0 {
		return files, remotePrefix, false, nil
	}
	// 目录前缀没有文件：尝试精确对象 key（单文件/顶层软链接上传场景）
	exactKey := strings.TrimSuffix(remotePrefix, "/")
	if exactKey == "" {
		return files, remotePrefix, false, nil
	}
	exactObjs, err := list(ctx, bucket, exactKey)
	if err != nil {
		return files, remotePrefix, false, nil // 精确探测失败不阻断，按空目录处理
	}
	for _, o := range exactObjs {
		if o.Key == exactKey && !(strings.HasSuffix(o.Key, "/") && o.Size == 0) {
			return []tos.ListedObjectV2{o}, exactKey, true, nil
		}
	}
	return files, remotePrefix, false, nil
}

// collectFiles 从对象列表中过滤目录占位对象，返回真正的文件列表。
func collectFiles(objs []tos.ListedObjectV2) []tos.ListedObjectV2 {
	files := make([]tos.ListedObjectV2, 0, len(objs))
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") && o.Size == 0 {
			continue
		}
		files = append(files, o)
	}
	return files
}
