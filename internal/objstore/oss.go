// oss.go — 阿里云 OSS 后端实现（封装 aliyun-oss-go-sdk）。
package objstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

func (b *ossBackend) Provider() string { return config.ProviderOSS }

// bucket 取指定 bucket 的操作句柄。
func (b *ossBackend) bucket(name string) (*oss.Bucket, error) {
	bk, err := b.client.Bucket(name)
	if err != nil {
		return nil, FriendlyError(err)
	}
	return bk, nil
}

// ListOnce 单次列出指定前缀下的对象（不分页）。
func (b *ossBackend) ListOnce(ctx context.Context, bucket, prefix string, maxKeys int) ([]Object, error) {
	bk, err := b.bucket(bucket)
	if err != nil {
		return nil, err
	}
	out, err := bk.ListObjectsV2(oss.WithContext(ctx), oss.Prefix(prefix), oss.MaxKeys(maxKeys))
	if err != nil {
		return nil, FriendlyError(err)
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
	return objs, nil
}

// ListAll 分页列出 bucket 中指定前缀下的所有对象。
// 注意：OSS 的 List 响应不含 CRC64，CRC64 恒为 0，需时由 Stat 补齐。
func (b *ossBackend) ListAll(ctx context.Context, bucket, prefix string) ([]Object, error) {
	bk, err := b.bucket(bucket)
	if err != nil {
		return nil, err
	}
	var all []Object
	token := ""
	for {
		out, err := bk.ListObjectsV2(oss.WithContext(ctx), oss.Prefix(prefix), oss.MaxKeys(1000), oss.ContinuationToken(token))
		if err != nil {
			return nil, FriendlyError(err)
		}
		for _, o := range out.Objects {
			all = append(all, Object{
				Key:          o.Key,
				Size:         o.Size,
				ETag:         o.ETag,
				LastModified: o.LastModified,
			})
		}
		if !out.IsTruncated || out.NextContinuationToken == "" {
			break
		}
		token = out.NextContinuationToken
	}
	return all, nil
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

// Stat 通过 GetObjectMeta(HEAD) 返回对象元信息（含 x-oss-hash-crc64ecma）。
func (b *ossBackend) Stat(ctx context.Context, bucket, key string) (Object, error) {
	bk, err := b.bucket(bucket)
	if err != nil {
		return Object{}, err
	}
	h, err := bk.GetObjectMeta(key, oss.WithContext(ctx))
	if err != nil {
		return Object{}, FriendlyError(err)
	}
	return Object{
		Key:          key,
		Size:         headerInt64(h, oss.HTTPHeaderContentLength),
		ETag:         h.Get(oss.HTTPHeaderEtag),
		CRC64:        headerUint64(h, oss.HTTPHeaderOssCRC64),
		LastModified: headerTime(h, oss.HTTPHeaderLastModified),
	}, nil
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

func headerInt64(h http.Header, key string) int64 {
	n, _ := strconv.ParseInt(h.Get(key), 10, 64)
	return n
}

func headerUint64(h http.Header, key string) uint64 {
	n, _ := strconv.ParseUint(h.Get(key), 10, 64)
	return n
}

func headerTime(h http.Header, key string) time.Time {
	t, _ := http.ParseTime(h.Get(key))
	return t
}
