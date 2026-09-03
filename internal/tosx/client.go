// Package tosx 封装火山云 TOS SDK，提供 aos 需要的客户端与便捷方法。
//
// 后端接入说明：当前绑定火山云 TOS SDK。未来接入 S3 等其他对象存储时，
// 在本层抽取后端接口即可，命令行与配置层无需改动。接入自建 S3 兼容存储
// （如 obs/minio/ceph）时务必开启 path-style 访问（AWS SDK 的 S3ForcePathStyle），
// 即用 endpoint/bucket/key 形式而非 bucket.endpoint 的 virtual-hosted style，
// 否则自建存储会解析失败（404）。火山云 TOS SDK 默认即 path-style，无需显式设置。
package tosx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/aos/internal/config"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// NewClient 根据配置创建 TOS 客户端。
// 开启 SDK 指数退避重试（最多重试 2 次，100ms/200ms），应对网络抖动与偶发失败。
// 火山云 TOS 使用 path-style 访问（endpoint/bucket/key），SDK 默认如此、无需显式配置；
// 若未来换用 AWS S3 SDK 接入自建 S3 兼容存储，需显式设置 S3ForcePathStyle=true。
func NewClient(cfg config.Config) (*tos.ClientV2, error) {
	client, err := tos.NewClientV2(cfg.EndpointOrDefault(),
		tos.WithRegion(cfg.Region),
		tos.WithCredentials(tos.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey)),
		tos.WithMaxRetryCount(2))
	if err != nil {
		return nil, fmt.Errorf("创建 TOS 客户端失败: %w", err)
	}
	return client, nil
}

// ListAll 分页列出 bucket 中指定前缀下的所有对象。
func ListAll(ctx context.Context, client *tos.ClientV2, bucket, prefix string) ([]tos.ListedObjectV2, error) {
	var all []tos.ListedObjectV2
	token := ""
	for {
		out, err := client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
			Bucket:            bucket,
			Prefix:            prefix,
			ContinuationToken: token,
			MaxKeys:           1000,
		})
		if err != nil {
			return nil, FriendlyError(err)
		}
		all = append(all, out.Contents...)
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

// UploadOne 上传单个文件。
// 小于 5MB 用单次 PUT，大文件用 SDK 分片上传（可开启断点续传）。
// partSize 为分片大小（0 用默认 20MB），taskNum 为单文件分片并发（0 用默认 4）。
// checkpointDir 非空时开启分片断点续传，checkpoint 文件由 SDK 生成在该目录下
// （命名 <文件名>.<bucket/key 哈希>.upload），避免写到源文件同目录污染数据。
func UploadOne(ctx context.Context, client *tos.ClientV2, bucket, key, localPath string, checkpoint bool, checkpointDir string, partSize int64, taskNum int) error {
	stat, err := statFile(localPath)
	if err != nil {
		return err
	}
	if stat.Size() < smallFileThreshold {
		_, err := client.PutObjectFromFile(ctx, &tos.PutObjectFromFileInput{
			PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: bucket, Key: key},
			FilePath:            localPath,
		})
		return FriendlyError(err)
	}
	input := &tos.UploadFileInput{
		CreateMultipartUploadV2Input: tos.CreateMultipartUploadV2Input{Bucket: bucket, Key: key},
		FilePath:                     localPath,
		PartSize:                     partSizeOrDefault(partSize),
		TaskNum:                      taskNumOrDefault(taskNum),
	}
	if checkpoint && checkpointDir != "" {
		// CheckpointFile 传已存在的目录时，SDK 会在其中生成按 bucket/key 哈希的 checkpoint 文件
		input.EnableCheckpoint = true
		input.CheckpointFile = checkpointDir
	}
	_, err = client.UploadFile(ctx, input)
	return FriendlyError(err)
}

// DownloadOne 下载单个对象到本地文件。
// size 为远端对象大小，用于选择单次 GET 还是分片下载（不依赖本地 dest 是否存在）。
// partSize 为分片大小（0 用默认 20MB），taskNum 为分片并发（0 用默认 4）。
// checkpointDir 非空时开启分片断点续传，checkpoint 文件由 SDK 生成在该目录下
// （命名 <文件名>.<bucket/key 哈希>.download）；下载完成后 SDK 自动清理。
func DownloadOne(ctx context.Context, client *tos.ClientV2, bucket, key, localPath string, size int64, partSize int64, taskNum int, checkpointDir string) error {
	if size < smallFileThreshold {
		_, err := client.GetObjectToFile(ctx, &tos.GetObjectToFileInput{
			GetObjectV2Input: tos.GetObjectV2Input{Bucket: bucket, Key: key},
			FilePath:         localPath,
		})
		return FriendlyError(err)
	}
	input := &tos.DownloadFileInput{
		HeadObjectV2Input: tos.HeadObjectV2Input{Bucket: bucket, Key: key},
		FilePath:          localPath,
		PartSize:          partSizeOrDefault(partSize),
		TaskNum:           taskNumOrDefault(taskNum),
	}
	if checkpointDir != "" {
		// CheckpointFile 传已存在的目录时，SDK 会在其中生成按 bucket/key 哈希的 checkpoint 文件
		input.EnableCheckpoint = true
		input.CheckpointFile = checkpointDir
	}
	_, err := client.DownloadFile(ctx, input)
	if err != nil && checkpointDir != "" && isCRCError(err) {
		// 仅断点续传场景：CRC64 校验失败说明本地 temp/checkpoint 状态损坏
		// （如磁盘故障），清掉残留后不带 checkpoint 全量重下一次，避免反复复用损坏状态
		_ = cleanupDownloadResidue(checkpointDir, localPath)
		retryInput := &tos.DownloadFileInput{
			HeadObjectV2Input: tos.HeadObjectV2Input{Bucket: bucket, Key: key},
			FilePath:          localPath,
			PartSize:          partSizeOrDefault(partSize),
			TaskNum:           taskNumOrDefault(taskNum),
		}
		if _, retryErr := client.DownloadFile(ctx, retryInput); retryErr == nil {
			return nil
		} else {
			return FriendlyError(retryErr)
		}
	}
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

// FriendlyError 将 SDK 错误转换成更易懂的中文提示（含权限建议）。
func FriendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Access Denied") || strings.Contains(msg, "AccessDenied"):
		return fmt.Errorf("TOS 访问被拒绝（Access Denied）。请确认账号对 bucket 具备相应权限（IAM 策略或桶策略），或运行 aos check 诊断: %w", err)
	case strings.Contains(msg, "does not exist"):
		return fmt.Errorf("目标 bucket 或对象不存在，请检查 bucket 名称与 region 是否匹配: %w", err)
	case strings.Contains(msg, "InvalidAccessKeyId") || strings.Contains(msg, "SignatureDoesNotMatch"):
		return fmt.Errorf("AccessKey / SecretKey 无效或不匹配，请检查配置（aos config 查看）: %w", err)
	default:
		return err
	}
}
