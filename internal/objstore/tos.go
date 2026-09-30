// tos.go — 火山云 TOS 后端实现（封装 ve-tos-golang-sdk）。
package objstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/aos/internal/config"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// tosBackend 火山云 TOS 后端。
type tosBackend struct {
	client *tos.ClientV2
}

// newTOSBackend 根据配置创建 TOS 客户端。
// 开启 SDK 指数退避重试（最多重试 2 次，100ms/200ms），应对网络抖动与偶发失败。
func newTOSBackend(cfg config.Config) (*tosBackend, error) {
	client, err := tos.NewClientV2(cfg.EndpointOrDefault(),
		tos.WithRegion(cfg.Region),
		tos.WithCredentials(tos.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey)),
		tos.WithMaxRetryCount(2))
	if err != nil {
		return nil, fmt.Errorf("创建 TOS 客户端失败: %w", err)
	}
	return &tosBackend{client: client}, nil
}

func (b *tosBackend) Provider() string { return config.ProviderTOS }

func tosObject(o tos.ListedObjectV2) Object {
	return Object{
		Key:          o.Key,
		Size:         o.Size,
		ETag:         o.ETag,
		CRC64:        o.HashCrc64ecma,
		LastModified: o.LastModified,
	}
}

// ListOnce 单次列出指定前缀下的对象（不分页）。
func (b *tosBackend) ListOnce(ctx context.Context, bucket, prefix string, maxKeys int) ([]Object, error) {
	out, err := b.client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
		Bucket:  bucket,
		Prefix:  prefix,
		MaxKeys: maxKeys,
	})
	if err != nil {
		return nil, FriendlyError(err)
	}
	objs := make([]Object, 0, len(out.Contents))
	for _, o := range out.Contents {
		objs = append(objs, tosObject(o))
	}
	return objs, nil
}

// ListAll 分页列出 bucket 中指定前缀下的所有对象。
func (b *tosBackend) ListAll(ctx context.Context, bucket, prefix string) ([]Object, error) {
	var all []Object
	token := ""
	for {
		out, err := b.client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
			Bucket:            bucket,
			Prefix:            prefix,
			ContinuationToken: token,
			MaxKeys:           1000,
		})
		if err != nil {
			return nil, FriendlyError(err)
		}
		for _, o := range out.Contents {
			all = append(all, tosObject(o))
		}
		if !out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
		if token == "" {
			break
		}
	}
	return all, nil
}

// PutFile 上传单个文件：小于 5MB 用单次 PUT，大文件用 SDK 分片上传（可开启断点续传）。
func (b *tosBackend) PutFile(ctx context.Context, bucket, key, localPath string, opt TransferOpt) error {
	stat, err := statFile(localPath)
	if err != nil {
		return err
	}
	if stat.Size() < smallFileThreshold {
		_, err := b.client.PutObjectFromFile(ctx, &tos.PutObjectFromFileInput{
			PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: bucket, Key: key},
			FilePath:            localPath,
		})
		return FriendlyError(err)
	}
	input := &tos.UploadFileInput{
		CreateMultipartUploadV2Input: tos.CreateMultipartUploadV2Input{Bucket: bucket, Key: key},
		FilePath:                     localPath,
		PartSize:                     partSizeOrDefault(opt.PartSize),
		TaskNum:                      taskNumOrDefault(opt.TaskNum),
	}
	if opt.CheckpointDir != "" {
		// CheckpointFile 传已存在的目录时，SDK 会在其中生成按 bucket/key 哈希的 checkpoint 文件
		input.EnableCheckpoint = true
		input.CheckpointFile = opt.CheckpointDir
	}
	_, err = b.client.UploadFile(ctx, input)
	return FriendlyError(err)
}

// PutBytes 上传一段文本内容作为对象（用于默认模式下软链接转同名文本文件）。
func (b *tosBackend) PutBytes(ctx context.Context, bucket, key string, data []byte) error {
	_, err := b.client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: bucket, Key: key},
		Content:             strings.NewReader(string(data)),
	})
	return FriendlyError(err)
}

// GetFile 下载单个对象到本地文件。
// size 为远端对象大小，用于选择单次 GET 还是分片下载（不依赖本地 dest 是否存在）。
func (b *tosBackend) GetFile(ctx context.Context, bucket, key, localPath string, size int64, opt TransferOpt) error {
	if size < smallFileThreshold {
		_, err := b.client.GetObjectToFile(ctx, &tos.GetObjectToFileInput{
			GetObjectV2Input: tos.GetObjectV2Input{Bucket: bucket, Key: key},
			FilePath:         localPath,
		})
		return FriendlyError(err)
	}
	input := &tos.DownloadFileInput{
		HeadObjectV2Input: tos.HeadObjectV2Input{Bucket: bucket, Key: key},
		FilePath:          localPath,
		PartSize:          partSizeOrDefault(opt.PartSize),
		TaskNum:           taskNumOrDefault(opt.TaskNum),
	}
	if opt.CheckpointDir != "" {
		input.EnableCheckpoint = true
		input.CheckpointFile = opt.CheckpointDir
	}
	_, err := b.client.DownloadFile(ctx, input)
	if err != nil && opt.CheckpointDir != "" && isCRCError(err) {
		// 仅断点续传场景：CRC64 校验失败说明本地 temp/checkpoint 状态损坏
		// （如磁盘故障），清掉残留后不带 checkpoint 全量重下一次，避免反复复用损坏状态
		_ = cleanupDownloadResidue(opt.CheckpointDir, localPath)
		retryInput := &tos.DownloadFileInput{
			HeadObjectV2Input: tos.HeadObjectV2Input{Bucket: bucket, Key: key},
			FilePath:          localPath,
			PartSize:          partSizeOrDefault(opt.PartSize),
			TaskNum:           taskNumOrDefault(opt.TaskNum),
		}
		if _, retryErr := b.client.DownloadFile(ctx, retryInput); retryErr == nil {
			return nil
		} else {
			return FriendlyError(retryErr)
		}
	}
	return FriendlyError(err)
}

// Stat 返回对象元信息（含 CRC64）。
func (b *tosBackend) Stat(ctx context.Context, bucket, key string) (Object, error) {
	out, err := b.client.HeadObjectV2(ctx, &tos.HeadObjectV2Input{Bucket: bucket, Key: key})
	if err != nil {
		return Object{}, FriendlyError(err)
	}
	return Object{
		Key:          key,
		Size:         out.ContentLength,
		ETag:         out.ETag,
		CRC64:        out.HashCrc64ecma,
		LastModified: out.LastModified,
	}, nil
}

// DeleteObject 删除单个对象（对象不存在也返回成功，幂等）。
func (b *tosBackend) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := b.client.DeleteObjectV2(ctx, &tos.DeleteObjectV2Input{Bucket: bucket, Key: key})
	return FriendlyError(err)
}

// DeleteObjects 批量删除（单批 ≤1000）；返回删除失败的对象 key。
func (b *tosBackend) DeleteObjects(ctx context.Context, bucket string, keys []string) (failed []string, err error) {
	if len(keys) == 0 {
		return nil, nil
	}
	objs := make([]tos.ObjectTobeDeleted, 0, len(keys))
	for _, k := range keys {
		objs = append(objs, tos.ObjectTobeDeleted{Key: k})
	}
	out, err := b.client.DeleteMultiObjects(ctx, &tos.DeleteMultiObjectsInput{
		Bucket:  bucket,
		Objects: objs,
		Quiet:   true,
	})
	if err != nil {
		return keys, FriendlyError(err)
	}
	for _, e := range out.Error {
		failed = append(failed, e.Key)
	}
	return failed, nil
}

// ListUploads 分页列出指定前缀下所有未完成的分片上传任务。
func (b *tosBackend) ListUploads(ctx context.Context, bucket, prefix string) ([]MultipartUpload, error) {
	var all []MultipartUpload
	keyMarker, uploadIDMarker := "", ""
	for {
		out, err := b.client.ListMultipartUploadsV2(ctx, &tos.ListMultipartUploadsV2Input{
			Bucket:         bucket,
			Prefix:         prefix,
			KeyMarker:      keyMarker,
			UploadIDMarker: uploadIDMarker,
			MaxUploads:     1000,
		})
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
func (b *tosBackend) AbortUpload(ctx context.Context, bucket, key, uploadID string) error {
	_, err := b.client.AbortMultipartUpload(ctx, &tos.AbortMultipartUploadInput{
		Bucket: bucket, Key: key, UploadID: uploadID,
	})
	return FriendlyError(err)
}

// isCRCError 判断是否为 SDK 的 CRC64 校验失败错误。
// SDK 在整文件 CRC64 与云端不一致时报 "tos: crc of entire file mismatch."。
func isCRCError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "crc")
}

// cleanupDownloadResidue 清理断点续传失败后残留的 checkpoint 与临时文件。
// checkpoint 文件名由 SDK 内部生成（<文件名>.<bucket/key 哈希>.download），
// 临时文件名为 <目标文件>.temp（SDK TempFileSuffix，曾出现过带时间戳后缀的旧实现），
// 这里按精确名/前缀匹配删除，不依赖 SDK 内部命名细节。
func cleanupDownloadResidue(checkpointDir, localPath string) error {
	var firstErr error
	remove := func(p string) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	base := filepath.Base(localPath)
	if entries, err := os.ReadDir(checkpointDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), base+".") && strings.HasSuffix(e.Name(), ".download") {
				remove(filepath.Join(checkpointDir, e.Name()))
			}
		}
	}
	// temp 文件与目标文件同目录：SDK 实际命名为 <目标文件>.temp
	if entries, err := os.ReadDir(filepath.Dir(localPath)); err == nil {
		for _, e := range entries {
			name := e.Name()
			if name == base+".temp" || strings.HasPrefix(name, base+".temp.") {
				remove(filepath.Join(filepath.Dir(localPath), name))
			}
		}
	}
	return firstErr
}
