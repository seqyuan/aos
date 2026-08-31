package tosx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/seqyuan/annotos/internal/config"
	"github.com/seqyuan/annotos/internal/human"
	"github.com/seqyuan/annotos/internal/spi"
	"github.com/seqyuan/annotos/internal/ui"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// DownloadOptions download 命令选项。
type DownloadOptions struct {
	Path          string // 直接指定 TOS 路径（tos://bucket/prefix 或 prefix），优先于 contract/spi
	Contract      string // 项目合同号，可为空（从 spi 推导）
	SPI           string // SPI 编号
	Name          string // 远端文件夹名，省略时自动探测
	LocalDir      string // 本地保存目录（支持相对路径），文件保存到 {LocalDir}/{name}/
	LocalDirExact bool   // 直接把 LocalDir 当作落盘根目录（不追加 name）
	Concurrency   int
	Overwrite     bool // 本地已存在同名文件时是否覆盖（默认跳过）
	Quiet         bool
	// OnCompleted 下载全部完成后回调（remotePrefix 为实际远端前缀，localRoot 为实际落盘根目录）。
	// 用于 down 后自动还原软链接等扩展逻辑。
	OnCompleted func(remotePrefix, localRoot string)
}

// Download 执行下载。
// 方式一：opt.Path 直接指定 TOS 路径（tos://bucket/prefix）
// 方式二：contract/spi/name 推导 remotePrefix；name 省略时自动探测
func Download(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt DownloadOptions, w io.Writer) error {
	var remotePrefix string
	var name string

	if opt.Path != "" {
		tp, err := ParseTOSPath(opt.Path, cfg.Bucket)
		if err != nil {
			return err
		}
		remotePrefix = tp.Prefix
		name = filepath.Base(strings.TrimSuffix(remotePrefix, "/"))
		if name == "." || name == "/" || name == "" {
			name = tp.Bucket
		}
	} else {
		if err := spi.ValidateSPI(opt.SPI); err != nil {
			return err
		}
		contract, err := resolveContract(opt.Contract, opt.SPI)
		if err != nil {
			return err
		}
		// 远端文件夹名：显式 -name 优先；否则列出 contract/spi/ 自动探测唯一子文件夹
		name = strings.TrimSpace(opt.Name)
		if name == "" {
			name, err = detectRemoteName(ctx, client, cfg.Bucket, contract, opt.SPI)
			if err != nil {
				return err
			}
		}
		remotePrefix = spi.TargetKey(contract, opt.SPI) + "/" + name + "/"
	}

	objs, err := ListAll(ctx, client, cfg.Bucket, remotePrefix)
	if err != nil {
		return err
	}

	// 过滤目录占位对象
	files := make([]tos.ListedObjectV2, 0, len(objs))
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") && o.Size == 0 {
			continue
		}
		files = append(files, o)
	}
	if len(files) == 0 {
		fmt.Fprintf(w, "远端 tos://%s/%s 下没有文件\n", cfg.Bucket, strings.TrimSuffix(remotePrefix, "/"))
		return nil
	}

	// 本地目标根目录：{LocalDir}/{name}，与上传对称（LocalDir 为空时即 ./name）
	// LocalDirExact 为 true 时直接使用 LocalDir 作为根目录（用于按 -d 路径还原下载）
	localRoot := filepath.Join(opt.LocalDir, name)
	if opt.LocalDirExact {
		localRoot = opt.LocalDir
	}
	var totalBytes int64
	type dlJob struct {
		key  string
		dest string
		size int64
	}
	var jobs []dlJob
	for _, o := range files {
		rel := strings.TrimPrefix(o.Key, remotePrefix)
		dest := filepath.Join(localRoot, filepath.FromSlash(rel))
		jobs = append(jobs, dlJob{key: o.Key, dest: dest, size: o.Size})
		totalBytes += o.Size
	}

	fmt.Fprintf(w, "下载 %d 个文件（共 %s）到 %s\n", len(jobs), human.Size(totalBytes), localRoot)

	// 跳过已存在（大小相同）的文件
	toDo := jobs[:0]
	for _, j := range jobs {
		if st, err := os.Stat(j.dest); err == nil {
			if st.Size() == j.size && !opt.Overwrite {
				fmt.Fprintf(w, "  跳过（已存在）%s\n", j.dest)
				continue
			}
		}
		toDo = append(toDo, j)
	}
	if len(toDo) == 0 {
		fmt.Fprintf(w, "所有文件已存在，无需下载 ✅\n")
		return nil
	}

	concurrency := opt.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency()
	}
	progress := ui.NewProgress(len(toDo), totalBytes, opt.Quiet, w)
	progress.Start()

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

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				if firstErr != nil {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(j.dest), 0o755); err != nil {
					reportErr(err)
					progress.Fail(j.key, err)
					continue
				}
				if err := DownloadOne(ctx, client, cfg.Bucket, j.key, j.dest); err != nil {
					reportErr(err)
					progress.Fail(j.key, err)
				} else {
					progress.Done(j.key, j.size)
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

	if firstErr != nil {
		return fmt.Errorf("下载失败: %w", firstErr)
	}
	fmt.Fprintf(w, "下载完成 ✅\n")
	if opt.OnCompleted != nil {
		opt.OnCompleted(strings.TrimSuffix(remotePrefix, "/"), localRoot)
	}
	return nil
}

// detectRemoteName 列出 contract/spi/ 下的一级子项，若只有一个非目录占位子项则返回其名字。
func detectRemoteName(ctx context.Context, client *tos.ClientV2, bucket, contract, spiID string) (string, error) {
	prefix := spi.TargetKey(contract, spiID) + "/"
	objs, err := ListAll(ctx, client, bucket, prefix)
	if err != nil {
		return "", err
	}
	set := map[string]bool{}
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") && o.Size == 0 {
			continue
		}
		rel := strings.TrimPrefix(o.Key, prefix)
		seg := rel
		if idx := strings.Index(seg, "/"); idx >= 0 {
			seg = seg[:idx]
		}
		if seg != "" {
			set[seg] = true
		}
	}
	switch len(set) {
	case 0:
		return "", fmt.Errorf("远端 %s 下没有文件，请检查 contract/spi 是否正确", strings.TrimSuffix(prefix, "/"))
	case 1:
		for k := range set {
			return k, nil
		}
	}
	return "", fmt.Errorf("远端 %s 下有多个子项 %v，请用 -name 指定要下载的文件夹", strings.TrimSuffix(prefix, "/"), keysOf(set))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
