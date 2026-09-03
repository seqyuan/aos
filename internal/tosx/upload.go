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

	"github.com/seqyuan/aos/internal/config"
	"github.com/seqyuan/aos/internal/human"
	"github.com/seqyuan/aos/internal/ui"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// UploadOptions 上传选项（aos cp <本地> tos://<bucket>/<前缀>）。
// 目标前缀直接铺入：每个文件的 key = <TargetPrefix> + 相对路径，不再自动拼目录名。
type UploadOptions struct {
	TargetPrefix string         // 目标 TOS 前缀（ParseTOSPath 解析结果，已带尾斜杠或为空）
	LocalPath    string         // 本地目录或文件，支持相对路径
	Concurrency  int            // 并发数
	Checkpoint   bool           // 大文件断点续传
	PartSize     int64          // 分片大小（0 用默认 20MB）
	TaskNum      int            // 单文件分片并发（0 用默认 4）
	FollowLinks  bool           // 软链接溯源上传真实内容（不记录到任务数据库）
	Exclude      []string       // 排除规则（glob）
	Quiet        bool           // 安静模式
	Recorder     UploadRecorder // 任务记录器（可为 nil）
}

// UploadRecorder 记录上传任务：调用方实现（例如写入 SQLite）。
// 返回的 taskID 会在后续回调中原样传回。
type UploadRecorder interface {
	OnTaskBegin(remotePrefix string, totalFiles int, totalBytes int64) (taskID int64, err error)
	OnProgress(taskID int64, doneFiles, doneBytes, failedFiles int64) error
	OnFinish(taskID int64, status, errMsg string) error
}

// Upload 执行上传。
func Upload(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt UploadOptions, w io.Writer) error {
	// 1. 解析本地路径（Lstat，不跟随软链接）
	localPath := filepath.Clean(opt.LocalPath)

	// 2. 目标前缀：用户指定的 tos:// 前缀（已带尾斜杠），去掉尾斜杠作为 key 基准
	basePrefix := strings.TrimSuffix(opt.TargetPrefix, "/")
	// 单文件上传必须落到具体对象 key：目标路径不能只到 bucket
	if st, err := os.Stat(localPath); err == nil && !st.IsDir() && basePrefix == "" {
		return fmt.Errorf("上传单个文件需指定对象 key（目标路径不能只到 bucket，如 tos://bucket/文件名）")
	}

	// 3. 收集待上传文件。
	// 默认：软链接不跟随、不上传（避免误传链接指向的共享大文件或造成循环）。
	// -follow-links：软链接溯源上传真实内容（链接在项目中的相对路径作为 key），
	// 且这些溯源文件不记录到任务数据库（不计入 total/done/failed 统计）。
	collected, err := collectUploadJobs(localPath, basePrefix, opt, w)
	if err != nil {
		return err
	}
	jobs := collected.jobs
	if len(jobs) == 0 {
		printLinkSkipSummary(w, collected)
		fmt.Fprintf(w, "没有需要上传的文件\n")
		return nil
	}

	// 5. 统计总大小。任务统计只含普通文件；溯源链接文件不记录。
	var totalBytes int64
	regularCount, followCount := 0, 0
	for i := range jobs {
		if st, err := os.Stat(jobs[i].local); err == nil {
			jobs[i].size = st.Size()
		}
		if jobs[i].followLink {
			followCount++
		} else {
			totalBytes += jobs[i].size
			regularCount++
		}
	}

	// 5.5 任务记录：开始（只记录普通文件，溯源链接文件不记录）
	taskID := int64(0)
	var recorder UploadRecorder = opt.Recorder
	if recorder != nil {
		id, err := recorder.OnTaskBegin(basePrefix, regularCount, totalBytes)
		if err != nil {
			fmt.Fprintf(w, "⚠️ 任务记录失败（继续上传，不记录）: %v\n", err)
			recorder = nil
		} else {
			taskID = id
		}
	}

	// 6. 打印计划
	fmt.Fprintf(w, "上传 %d 个文件（共 %s）到 tos://%s/%s\n",
		regularCount, human.Size(totalBytes), cfg.Bucket, basePrefix)
	if followCount > 0 {
		fmt.Fprintf(w, "另溯源上传 %d 个链接文件（内容为链接目标，不记录任务）\n", followCount)
	}
	printLinkSkipSummary(w, collected)

	// 7. 并发上传
	concurrency := opt.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency()
	}
	// 上传断点续传：checkpoint 集中存于上传根目录 .aos/checkpoints/
	// （.aos 默认跳过，不会把 checkpoint 残留当数据上传；与下载目录结构对称）
	checkpointDir := ""
	if opt.Checkpoint {
		root := localPath
		if st, err := os.Stat(localPath); err == nil && !st.IsDir() {
			root = filepath.Dir(localPath)
		}
		checkpointDir = filepath.Join(root, ".aos", "checkpoints")
		if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
			checkpointDir = "" // 无法创建目录时退化为不带 checkpoint
		}
	}
	progress := ui.NewProgress(regularCount, totalBytes, opt.Quiet, w)
	progress.Start()

	var wg sync.WaitGroup
	jobCh := make(chan uploadJob)
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
	// 溯源链接文件统计（不记录任务，仅用于总结提示）
	var followMu sync.Mutex
	followDone, followFail, followSkip, followBytes := 0, 0, 0, int64(0)
	countFollow := func(done, failed, skipped int, bytes int64) {
		followMu.Lock()
		followDone += done
		followFail += failed
		followSkip += skipped
		followBytes += bytes
		followMu.Unlock()
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
				if j.followLink {
					// 溯源链接文件：不记录任务数据库/进度；失败仅提示、不中断任务
					if cancelled() {
						// 前序普通文件已失败：溯源链接不执行，计数并在总结中提示（不中断、不记录任务）
						countFollow(0, 0, 1, 0)
						continue
					}
					err := UploadOne(ctx, client, cfg.Bucket, j.key, j.local, opt.Checkpoint, checkpointDir, opt.PartSize, opt.TaskNum)
					if err != nil {
						countFollow(0, 1, 0, 0)
						followMu.Lock()
						fmt.Fprintf(w, "  ⚠️ 溯源链接上传失败 %s: %v\n", j.key, err)
						followMu.Unlock()
					} else {
						countFollow(1, 0, 0, j.size)
					}
					continue
				}
				if cancelled() {
					// 前序文件已失败：剩余任务不执行（未执行不计入失败数，仅提示跳过）
					progress.Skip(j.key)
					countSkip()
					continue
				}
				err := UploadOne(ctx, client, cfg.Bucket, j.key, j.local, opt.Checkpoint, checkpointDir, opt.PartSize, opt.TaskNum)
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

	if skippedCount > 0 {
		fmt.Fprintf(w, "其余 %d 个文件已跳过（因前序失败，未执行）\n", skippedCount)
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
		return fmt.Errorf("上传失败: %w", firstErr)
	}
	if followDone > 0 || followFail > 0 || followSkip > 0 {
		fmt.Fprintf(w, "溯源上传完成：%d 个链接文件（共 %s）", followDone, human.Size(followBytes))
		if followFail > 0 {
			fmt.Fprintf(w, "，失败 %d 个", followFail)
		}
		if followSkip > 0 {
			fmt.Fprintf(w, "，跳过 %d 个（因前序失败未执行）", followSkip)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "上传完成 ✅\n")
	return nil
}

type uploadJob struct {
	local      string // 本地文件路径（溯源链接时为链接目标的真实路径）
	key        string
	size       int64
	followLink bool // 溯源链接文件：内容为链接目标，不记录任务数据库
}

// collectedJobs 收集结果。
type collectedJobs struct {
	jobs         []uploadJob
	skippedLinks int // 默认模式跳过的软链接数
	brokenLinks  int // 断链/无效软链接数（-follow-links 时）
	skippedTmp   int // 默认跳过的 *.tmp/*.checkpoint 文件数
}

// printLinkSkipSummary 打印软链接跳过/断链统计（收集完成或计划阶段均调用）。
func printLinkSkipSummary(w io.Writer, c collectedJobs) {
	if c.skippedLinks > 0 {
		fmt.Fprintf(w, "跳过 %d 个软链接（未开启 -follow-links）\n", c.skippedLinks)
	}
	if c.brokenLinks > 0 {
		fmt.Fprintf(w, "跳过 %d 个断链/无效软链接\n", c.brokenLinks)
	}
	if c.skippedTmp > 0 {
		fmt.Fprintf(w, "跳过 %d 个 *.tmp/*.checkpoint 后缀文件（默认不上传；如确需上传请改名）\n", c.skippedTmp)
	}
}

// collectUploadJobs 遍历本地路径收集上传任务。
// FollowLinks 关闭时软链接直接跳过；开启时软链接溯源上传链接目标的真实内容
// （key 仍用链接在项目中的相对路径），目录链接递归展开并按 realpath 防循环，断链跳过并提示。
func collectUploadJobs(localPath, basePrefix string, opt UploadOptions, w io.Writer) (collectedJobs, error) {
	// 归一为绝对路径：避免 -d 用相对路径时，EvalSymlinks/后续 os.Stat 依赖进程 cwd。
	if abs, err := filepath.Abs(localPath); err == nil {
		localPath = abs
	}
	c := &collector{opt: opt, w: w, visited: map[string]bool{}}
	lstat, err := os.Lstat(localPath)
	if err != nil {
		return collectedJobs{}, fmt.Errorf("无法访问本地路径 %s: %w", localPath, err)
	}
	switch {
	case lstat.Mode()&os.ModeSymlink != 0:
		if !opt.FollowLinks {
			return collectedJobs{}, fmt.Errorf("不支持直接上传软链接 %s（可用 -follow-links 溯源上传链接指向的内容）", localPath)
		}
		// 顶层链接：溯源上传（目录链接展开到 basePrefix 下）
		if err := c.addSymlinkTarget(localPath, "", basePrefix); err != nil {
			return collectedJobs{}, err
		}
	case !lstat.IsDir():
		c.jobs = append(c.jobs, uploadJob{local: localPath, key: normalizeKey(basePrefix)})
	default:
		if err := c.walkDir(localPath, localPath, basePrefix, false); err != nil {
			return collectedJobs{}, fmt.Errorf("遍历本地目录 %s 失败: %w", localPath, err)
		}
	}
	return collectedJobs{jobs: c.jobs, skippedLinks: c.skippedLinks, brokenLinks: c.brokenLinks, skippedTmp: c.skippedTmp}, nil
}

// collector 收集上传任务；visited 记录已展开的目录 realpath 用于防循环。
type collector struct {
	opt          UploadOptions
	w            io.Writer
	jobs         []uploadJob
	visited      map[string]bool
	skippedLinks int
	brokenLinks  int
	skippedTmp   int
}

// walkDir 递归遍历目录 dir，把其下文件映射到 keyPrefix 前缀下。
// root 为计算相对路径的基准；follow 表示是否处于“链接展开”上下文（其内所有文件都不记录任务）。
func (c *collector) walkDir(root, dir, keyPrefix string, follow bool) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := normalizeKey(filepath.ToSlash(rel))
		key := normalizeKey(keyPrefix + "/" + relSlash)
		// 内置默认跳过项 + 用户 -exclude 规则
		if defaultSkip(info.Name(), info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			if isTmpSuffix(info.Name()) {
				c.skippedTmp++
			}
			return nil
		}
		if info.IsDir() {
			if ExcludeMatch(relSlash, info.Name(), c.opt.Exclude) {
				return filepath.SkipDir
			}
			return nil
		}
		if ExcludeMatch(relSlash, info.Name(), c.opt.Exclude) {
			return nil
		}
		if isSymlink(path) {
			if !c.opt.FollowLinks {
				c.skippedLinks++
				return nil
			}
			return c.addSymlinkTarget(path, relSlash, keyPrefix)
		}
		c.jobs = append(c.jobs, uploadJob{local: path, key: key, followLink: follow})
		return nil
	})
}

// addSymlinkTarget 溯源处理软链接 path：文件链接上传目标内容（key 为链接路径），
// 目录链接递归展开；断链/循环跳过并提示。
func (c *collector) addSymlinkTarget(path, relSlash, keyPrefix string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		c.brokenLinks++
		fmt.Fprintf(c.w, "  - 跳过断链软链接 %s: %v\n", path, err)
		return nil
	}
	st, err := os.Stat(real)
	if err != nil {
		c.brokenLinks++
		fmt.Fprintf(c.w, "  - 跳过无效软链接 %s: %v\n", path, err)
		return nil
	}
	if !st.IsDir() {
		key := normalizeKey(keyPrefix + "/" + relSlash)
		c.jobs = append(c.jobs, uploadJob{local: real, key: key, followLink: true})
		return nil
	}
	// 目录链接：递归展开
	if c.visited[real] {
		fmt.Fprintf(c.w, "  - 跳过循环软链接 %s -> %s\n", path, real)
		return nil
	}
	c.visited[real] = true
	return c.walkDir(real, real, keyPrefix+"/"+relSlash, true)
}

// isSymlink 判断路径是否为软链接（用 Lstat，不跟随）。
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// normalizeKey 规范化 TOS key：去掉空段、"." 与 ".." 段，折叠连续 "/"。
// 例如 "./abc//de/./f" -> "abc/de/f"；"a/../b" -> "a/b"（丢弃 ..，不向上跳出）。
func normalizeKey(key string) string {
	parts := strings.Split(key, "/")
	out := parts[:0]
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "/")
}

// defaultSkip 内置默认跳过的文件/目录（可被 -exclude 追加，不冲突）。
func defaultSkip(name string, isDir bool) bool {
	switch name {
	case ".git", ".DS_Store", ".aos", ".svn", "__pycache__", ".ipynb_checkpoints":
		return true
	}
	if strings.HasPrefix(name, "._") {
		return true
	}
	return isTmpSuffix(name)
}

// isTmpSuffix 判断是否为临时/断点文件后缀（*.tmp / *.checkpoint）。
func isTmpSuffix(name string) bool {
	return strings.HasSuffix(name, ".checkpoint") || strings.HasSuffix(name, ".tmp")
}

// defaultConcurrency 返回默认文件级并发数。
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
