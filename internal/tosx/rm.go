// rm.go — aos rm：删除 TOS 对象与未完成分片上传任务。
// 与 Download/Upload 同构：RM 构造真实存储操作，rmExecute 是注入化的可测编排。
package tosx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/seqyuan/aos/internal/config"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// deleteBatchSize 批量删除单次请求的对象数上限（S3/TOS 限制 1000）。
const deleteBatchSize = 1000

// RMOptions rm 选项。
// Path 无 -r 时为精确对象 key（tos://bucket/dir/file.txt）；有 -r 时是前缀（tos://bucket/dir）。
type RMOptions struct {
	Path      string
	Recursive bool // -r：递归删除前缀下所有对象，并顺带 abort 该前缀未完成分片上传
	Force     bool // -f：跳过批量删除前的确认
	Quiet     bool // -q
	// Confirm 批量删除前的确认交互（默认读终端）；仅 -r 且非 -f 时使用。
	Confirm func(prompt string) (bool, error)
}

// RMResult 删除结果（供报告输出）。
type RMResult struct {
	DeletedObjects int // 成功删除的对象数
	FailedObjects  int // 删除失败的对象数
	AbortedUploads int // 成功清理的分片上传任务数
	// AbortFailed 记录清理失败的分片上传数
	AbortFailed int
}

// rmOps 把删除流程对存储的依赖注入化，便于单测。
type rmOps struct {
	listObjects func(ctx context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error)
	deleteOne   func(ctx context.Context, bucket, key string) error
	// deleteBatch 批量删除；返回删除失败的对象 key（删除不存在的对象视为成功）。
	deleteBatch func(ctx context.Context, bucket string, keys []string) []string
	listUploads func(ctx context.Context, bucket, prefix string) ([]tos.ListedUpload, error)
	abortUpload func(ctx context.Context, bucket, key, uploadID string) error
}

// RM 执行删除。
func RM(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt RMOptions, w io.Writer) (RMResult, error) {
	tp, err := ParseTOSPath(opt.Path, cfg.Bucket)
	if err != nil {
		return RMResult{}, err
	}
	ops := rmOps{
		listObjects: func(c context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error) {
			return ListAll(c, client, bucket, prefix)
		},
		deleteOne: func(c context.Context, bucket, key string) error {
			_, err := client.DeleteObjectV2(c, &tos.DeleteObjectV2Input{Bucket: bucket, Key: key})
			return err
		},
		deleteBatch: func(c context.Context, bucket string, keys []string) []string {
			// 按 1000 分批；并发删除各批（worker 数同 defaultConcurrency），失败聚合返回。
			var batches [][]string
			for start := 0; start < len(keys); start += deleteBatchSize {
				end := start + deleteBatchSize
				if end > len(keys) {
					end = len(keys)
				}
				batches = append(batches, keys[start:end])
			}
			if len(batches) == 0 {
				return nil
			}
			workers := defaultConcurrency()
			if workers > len(batches) {
				workers = len(batches)
			}
			var (
				wg     sync.WaitGroup
				mu     sync.Mutex
				failed []string
			)
			ch := make(chan []string)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for batch := range ch {
						objs := make([]tos.ObjectTobeDeleted, 0, len(batch))
						for _, k := range batch {
							objs = append(objs, tos.ObjectTobeDeleted{Key: k})
						}
						out, err := client.DeleteMultiObjects(c, &tos.DeleteMultiObjectsInput{
							Bucket:  bucket,
							Objects: objs,
							Quiet:   true,
						})
						var batchFailed []string
						if err != nil {
							batchFailed = batch
						} else {
							for _, e := range out.Error {
								batchFailed = append(batchFailed, e.Key)
							}
						}
						if len(batchFailed) > 0 {
							mu.Lock()
							failed = append(failed, batchFailed...)
							mu.Unlock()
						}
					}
				}()
			}
			for _, b := range batches {
				ch <- b
			}
			close(ch)
			wg.Wait()
			return failed
		},
		listUploads: func(c context.Context, bucket, prefix string) ([]tos.ListedUpload, error) {
			return listMultipartUploads(c, client, bucket, prefix)
		},
		abortUpload: func(c context.Context, bucket, key, uploadID string) error {
			_, err := client.AbortMultipartUpload(c, &tos.AbortMultipartUploadInput{
				Bucket: bucket, Key: key, UploadID: uploadID,
			})
			return err
		},
	}
	return rmExecute(ctx, ops, tp.Bucket, tp.Prefix, opt, w)
}

// rmExecute 删除编排（不依赖具体 client，可注入测试）。
func rmExecute(ctx context.Context, ops rmOps, bucket, prefix string, opt RMOptions, w io.Writer) (RMResult, error) {
	// ---- 单对象删除（精确 key）：直接删，幂等，不询问 ----
	if !opt.Recursive {
		key := strings.TrimSuffix(prefix, "/")
		if key == "" {
			return RMResult{}, fmt.Errorf("请输入要删除的对象 key（如 tos://bucket/dir/file.txt；或加 -r 递归删除前缀下所有对象）")
		}
		if err := ops.deleteOne(ctx, bucket, key); err != nil {
			// DeleteObject 幂等：对象不存在也返回 204 成功，无需特判 NotFound。
			return RMResult{}, FriendlyError(err)
		}
		if !opt.Quiet {
			fmt.Fprintf(w, "已删除 1 个对象 ✅\n")
		}
		return RMResult{DeletedObjects: 1}, nil
	}

	// ---- 递归删除（-r）：列对象 + 列分片 → 确认 → 删对象 + abort 分片 ----
	objs, err := ops.listObjects(ctx, bucket, prefix)
	if err != nil {
		return RMResult{}, FriendlyError(err)
	}
	files := collectFiles(objs) // 过滤目录占位对象（key 以 / 结尾且 size 0）
	uploads, err := ops.listUploads(ctx, bucket, prefix)
	if err != nil {
		return RMResult{}, FriendlyError(err)
	}
	if len(files) == 0 && len(uploads) == 0 {
		if !opt.Quiet {
			fmt.Fprintf(w, "tos://%s/%s 下没有对象或未完成分片上传任务\n", bucket, strings.TrimSuffix(prefix, "/"))
		}
		return RMResult{}, nil
	}

	wholeBucket := strings.TrimSuffix(prefix, "/") == ""

	// 确认（-f 跳过；非终端默认拒绝）
	if !opt.Force {
		confirm := opt.Confirm
		if confirm == nil {
			confirm = defaultConfirm
		}
		prompt := rmConfirmPrompt(bucket, wholeBucket, len(files), len(uploads))
		ok, err := confirm(prompt)
		if err != nil {
			return RMResult{}, err
		}
		if !ok {
			if !opt.Quiet {
				fmt.Fprintln(w, "已取消")
			}
			return RMResult{}, nil
		}
	} else if wholeBucket && !opt.Quiet {
		fmt.Fprintf(w, "警告: 将删除整个 bucket %s 下的全部对象\n", bucket)
	}

	// 删除对象（分批 ≤1000，失败不中断）
	keys := make([]string, 0, len(files))
	for _, o := range files {
		keys = append(keys, o.Key)
	}
	failedKeys := ops.deleteBatch(ctx, bucket, keys)

	// 清理未完成分片上传（失败不中断，仅计数）
	aborted, abortFailed := 0, 0
	for _, up := range uploads {
		if err := ops.abortUpload(ctx, bucket, up.Key, up.UploadID); err != nil {
			abortFailed++
			continue
		}
		aborted++
	}

	res := RMResult{
		DeletedObjects: len(keys) - len(failedKeys),
		FailedObjects:  len(failedKeys),
		AbortedUploads: aborted,
		AbortFailed:    abortFailed,
	}
	// 报告：不逐条打印，只汇总
	if !opt.Quiet {
		fmt.Fprintf(w, "已删除 %d 个对象", res.DeletedObjects)
		if res.FailedObjects > 0 {
			fmt.Fprintf(w, "，失败 %d 个", res.FailedObjects)
		}
		if res.AbortedUploads > 0 || res.AbortFailed > 0 {
			fmt.Fprintf(w, "；清理 %d 个未完成分片上传任务", res.AbortedUploads)
			if res.AbortFailed > 0 {
				fmt.Fprintf(w, "，%d 个清理失败", res.AbortFailed)
			}
		}
		if res.FailedObjects == 0 && res.AbortFailed == 0 {
			fmt.Fprintf(w, " ✅")
		}
		fmt.Fprintln(w)
	}
	if res.FailedObjects > 0 {
		retry := opt.Path
		if retry == "" {
			retry = "tos://" + bucket + "/" + strings.TrimSuffix(prefix, "/")
		}
		return res, fmt.Errorf("删除失败 %d 个对象（已删除 %d 个；重试 aos rm %s -r -f 可继续删除剩余对象）",
			res.FailedObjects, res.DeletedObjects, retry)
	}
	if res.AbortFailed > 0 {
		return res, fmt.Errorf("清理失败 %d 个未完成分片上传任务（已清理 %d 个）",
			res.AbortFailed, res.AbortedUploads)
	}
	return res, nil
}

// rmConfirmPrompt 构造 -r 删除前的确认文案。空前缀视为整桶。
func rmConfirmPrompt(bucket string, wholeBucket bool, nFiles, nUploads int) string {
	if wholeBucket {
		switch {
		case nFiles > 0 && nUploads > 0:
			return fmt.Sprintf("将删除整个 bucket %s 中的 %d 个对象、清理 %d 个未完成分片上传任务。确认? (y/N) ", bucket, nFiles, nUploads)
		case nFiles > 0:
			return fmt.Sprintf("将删除整个 bucket %s 中的 %d 个对象。确认? (y/N) ", bucket, nFiles)
		default:
			return fmt.Sprintf("将清理整个 bucket %s 中的 %d 个未完成分片上传任务。确认? (y/N) ", bucket, nUploads)
		}
	}
	switch {
	case nFiles > 0 && nUploads > 0:
		return fmt.Sprintf("将删除 %d 个对象、清理 %d 个未完成分片上传任务。确认? (y/N) ", nFiles, nUploads)
	case nFiles > 0:
		return fmt.Sprintf("将删除 %d 个对象。确认? (y/N) ", nFiles)
	default:
		return fmt.Sprintf("将清理 %d 个未完成分片上传任务。确认? (y/N) ", nUploads)
	}
}

// listMultipartUploads 分页列出指定前缀下所有未完成的分片上传任务。
func listMultipartUploads(ctx context.Context, client *tos.ClientV2, bucket, prefix string) ([]tos.ListedUpload, error) {
	var all []tos.ListedUpload
	keyMarker, uploadIDMarker := "", ""
	for {
		out, err := client.ListMultipartUploadsV2(ctx, &tos.ListMultipartUploadsV2Input{
			Bucket:         bucket,
			Prefix:         prefix,
			KeyMarker:      keyMarker,
			UploadIDMarker: uploadIDMarker,
			MaxUploads:     1000,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, out.Uploads...)
		if !out.IsTruncated {
			break
		}
		keyMarker, uploadIDMarker = out.NextKeyMarker, out.NextUploadIDMarker
		if keyMarker == "" && uploadIDMarker == "" {
			break
		}
	}
	return all, nil
}

// defaultConfirm 默认确认交互：终端打印提示并读取一行 y/yes。
// 非终端（管道/脚本）环境下拒绝并提示加 -f，防止误删。
func defaultConfirm(prompt string) (bool, error) {
	if !isTerminal(os.Stdin) {
		return false, fmt.Errorf("非终端环境无法交互确认；如确认要删除请加 -f 跳过确认")
	}
	fmt.Fprint(os.Stdout, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// isTerminal 判断 r 是否为字符设备（终端）。
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
