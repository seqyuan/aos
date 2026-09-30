// oss.go — 阿里云 OSS 后端实现（封装 aliyun-oss-go-sdk）。
package objstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/seqyuan/aos/internal/config"
)

// ossBackend 阿里云 OSS 后端。
type ossBackend struct {
	client *oss.Client
}

// newOSSBackend 根据配置创建 OSS 客户端。
// OSS 签名与 region 无关（endpoint 已含 region），故无需 WithRegion。
// 安全默认：endpoint 未带 scheme 时用 https（OSS SDK 裸 host 默认 http）；
// 内网 endpoint 如需 http，可显式写成 http:// 前缀。
func newOSSBackend(cfg config.Config) (*ossBackend, error) {
	ep := cfg.EndpointOrDefault()
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	client, err := oss.New(ep, cfg.AccessKey, cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("创建 OSS 客户端失败: %w", err)
	}
	return &ossBackend{client: client}, nil
}

// bucket 取指定 bucket 的操作句柄。
func (b *ossBackend) bucket(name string) (*oss.Bucket, error) {
	bk, err := b.client.Bucket(name)
	if err != nil {
		return nil, FriendlyError(err)
	}
	return bk, nil
}

// ListPage 列出指定前缀下的对象（单页）。
func (b *ossBackend) ListPage(ctx context.Context, bucket, prefix string, maxKeys int, token string) ([]Object, string, error) {
	bk, err := b.bucket(bucket)
	if err != nil {
		return nil, "", err
	}
	out, err := bk.ListObjectsV2(oss.WithContext(ctx), oss.Prefix(prefix), oss.MaxKeys(maxKeys), oss.ContinuationToken(token))
	if err != nil {
		return nil, "", FriendlyError(err)
	}
	objs := make([]Object, 0, len(out.Objects))
	for _, o := range out.Objects {
		objs = append(objs, Object{
			Key:          o.Key,
			Size:         o.Size,
			ETag:         o.ETag,
			LastModified: o.LastModified,
		})
	}
	if !out.IsTruncated {
		return objs, "", nil
	}
	return objs, out.NextContinuationToken, nil
}

// Fingerprints 返回 keys 中确实存在的对象的 {size, crc64}。
// OSS 的 List 响应不含 CRC64，故先 List 确定存在性与大小，再仅对命中 key
// 并发 HEAD 取 x-oss-hash-crc64ecma（新增文件不 HEAD，一次 List 即可判定不存在）。
// HEAD 失败/无 crc 时该 key 的 CRC64 为 0，调用方会保守上传。
func (b *ossBackend) Fingerprints(ctx context.Context, bucket, prefix string, keys []string) (map[string]Fingerprint, error) {
	objs, err := ListAll(ctx, b, bucket, prefix)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make(map[string]Fingerprint, len(keys))
	var toHead []string
	for _, o := range objs {
		if want[o.Key] {
			out[o.Key] = Fingerprint{Size: o.Size}
			toHead = append(toHead, o.Key)
		}
	}
	if len(toHead) == 0 {
		return out, nil
	}
	bk, err := b.bucket(bucket)
	if err != nil {
		return nil, err
	}

	workers := defaultConcurrency()
	if workers > len(toHead) {
		workers = len(toHead)
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	ch := make(chan string)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range ch {
				h, err := bk.GetObjectMeta(key, oss.WithContext(ctx))
				if err != nil {
					continue // HEAD 失败：crc64 保持 0，保守上传
				}
				mu.Lock()
				fp := out[key]
				fp.CRC64 = headerUint64(h, oss.HTTPHeaderOssCRC64)
				out[key] = fp
				mu.Unlock()
			}
		}()
	}
	for _, k := range toHead {
		ch <- k
	}
	close(ch)
	wg.Wait()
	return out, nil
}

// PutFile 上传单个文件：小于 5MB 用单次 PUT，大文件用 SDK 分片上传（可开启断点续传）。
func (b *ossBackend) PutFile(ctx context.Context, bucket, key, localPath string, opt TransferOpt) error {
	stat, err := statFile(localPath)
	if err != nil {
		return err
	}
	bk, err := b.bucket(bucket)
	if err != nil {
		return err
	}
	if stat.Size() < smallFileThreshold {
		return FriendlyError(bk.PutObjectFromFile(key, localPath, oss.WithContext(ctx)))
	}
	opts := []oss.Option{oss.WithContext(ctx), oss.Routines(taskNumOrDefault(opt.TaskNum))}
	if opt.CheckpointDir != "" {
		opts = append(opts, oss.CheckpointDir(true, opt.CheckpointDir))
	}
	return FriendlyError(bk.UploadFile(key, localPath, partSizeOrDefault(opt.PartSize), opts...))
}

// PutBytes 上传一段内容作为对象（用于默认模式下软链接转同名文本文件）。
func (b *ossBackend) PutBytes(ctx context.Context, bucket, key string, data []byte) error {
	bk, err := b.bucket(bucket)
	if err != nil {
		return err
	}
	return FriendlyError(bk.PutObject(key, bytes.NewReader(data), oss.WithContext(ctx)))
}

// GetFile 下载单个对象到本地文件；size 为远端对象大小（用于选择单次/分片）。
func (b *ossBackend) GetFile(ctx context.Context, bucket, key, localPath string, size int64, opt TransferOpt) error {
	bk, err := b.bucket(bucket)
	if err != nil {
		return err
	}
	if size < smallFileThreshold {
		return FriendlyError(bk.GetObjectToFile(key, localPath, oss.WithContext(ctx)))
	}
	opts := []oss.Option{oss.WithContext(ctx), oss.Routines(taskNumOrDefault(opt.TaskNum))}
	if opt.CheckpointDir != "" {
		opts = append(opts, oss.CheckpointDir(true, opt.CheckpointDir))
	}
	return FriendlyError(bk.DownloadFile(key, localPath, partSizeOrDefault(opt.PartSize), opts...))
}

// DeleteObject 删除单个对象（对象不存在也返回成功，幂等）。
func (b *ossBackend) DeleteObject(ctx context.Context, bucket, key string) error {
	bk, err := b.bucket(bucket)
	if err != nil {
		return err
	}
	return FriendlyError(bk.DeleteObject(key, oss.WithContext(ctx)))
}

// DeleteObjects 批量删除（单批 ≤1000）；返回删除失败的对象 key。
// OSS 批量删除 200 也可能带部分失败，按「返回的已删除集合」与请求集合求差得到失败项。
func (b *ossBackend) DeleteObjects(ctx context.Context, bucket string, keys []string) (failed []string, err error) {
	if len(keys) == 0 {
		return nil, nil
	}
	bk, err := b.bucket(bucket)
	if err != nil {
		return keys, err
	}
	out, err := bk.DeleteObjects(keys, oss.WithContext(ctx))
	if err != nil {
		return keys, FriendlyError(err)
	}
	deleted := make(map[string]bool, len(out.DeletedObjects))
	for _, k := range out.DeletedObjects {
		deleted[k] = true
	}
	for _, k := range keys {
		if !deleted[k] {
			failed = append(failed, k)
		}
	}
	return failed, nil
}

// ListUploads 分页列出指定前缀下所有未完成的分片上传任务。
func (b *ossBackend) ListUploads(ctx context.Context, bucket, prefix string) ([]MultipartUpload, error) {
	bk, err := b.bucket(bucket)
	if err != nil {
		return nil, err
	}
	var all []MultipartUpload
	keyMarker, uploadIDMarker := "", ""
	for {
		out, err := bk.ListMultipartUploads(
			oss.WithContext(ctx),
			oss.Prefix(prefix),
			oss.MaxUploads(1000),
			oss.KeyMarker(keyMarker),
			oss.UploadIDMarker(uploadIDMarker),
		)
		if err != nil {
			return nil, FriendlyError(err)
		}
		for _, up := range out.Uploads {
			all = append(all, MultipartUpload{Key: up.Key, UploadID: up.UploadID})
		}
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

// AbortUpload 取消一个未完成的分片上传任务。
func (b *ossBackend) AbortUpload(ctx context.Context, bucket, key, uploadID string) error {
	bk, err := b.bucket(bucket)
	if err != nil {
		return err
	}
	return FriendlyError(bk.AbortMultipartUpload(oss.InitiateMultipartUploadResult{
		Bucket: bucket, Key: key, UploadID: uploadID,
	}, oss.WithContext(ctx)))
}

func headerUint64(h http.Header, key string) uint64 {
	n, _ := strconv.ParseUint(h.Get(key), 10, 64)
	return n
}
