// Package tosx 封装火山云 TOS SDK，提供 annotos 需要的客户端与便捷方法。
package tosx

import (
	"context"
	"fmt"
	"strings"

	"github.com/seqyuan/annotos/internal/config"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// NewClient 根据配置创建 TOS 客户端。
func NewClient(cfg config.Config) (*tos.ClientV2, error) {
	client, err := tos.NewClientV2(cfg.EndpointOrDefault(),
		tos.WithRegion(cfg.Region),
		tos.WithCredentials(tos.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey)))
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
func UploadOne(ctx context.Context, client *tos.ClientV2, bucket, key, localPath string, checkpoint bool) error {
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
	_, err = client.UploadFile(ctx, &tos.UploadFileInput{
		CreateMultipartUploadV2Input: tos.CreateMultipartUploadV2Input{Bucket: bucket, Key: key},
		FilePath:                     localPath,
		PartSize:                     defaultPartSize,
		TaskNum:                      multipartTaskNum,
		EnableCheckpoint:             checkpoint,
	})
	return FriendlyError(err)
}

// DownloadOne 下载单个对象到本地文件。
func DownloadOne(ctx context.Context, client *tos.ClientV2, bucket, key, localPath string) error {
	stat, err := statFile(localPath)
	if err == nil && stat.Size() < smallFileThreshold {
		_, err := client.GetObjectToFile(ctx, &tos.GetObjectToFileInput{
			GetObjectV2Input: tos.GetObjectV2Input{Bucket: bucket, Key: key},
			FilePath:         localPath,
		})
		return FriendlyError(err)
	}
	if err != nil && !isNotExist(err) {
		return err
	}
	_, err = client.DownloadFile(ctx, &tos.DownloadFileInput{
		HeadObjectV2Input: tos.HeadObjectV2Input{Bucket: bucket, Key: key},
		FilePath:          localPath,
		PartSize:          defaultPartSize,
		TaskNum:           multipartTaskNum,
	})
	return FriendlyError(err)
}

// UploadText 上传一段文本内容为对象（用于软链接转文本文件）。
func UploadText(ctx context.Context, client *tos.ClientV2, bucket, key, content string) error {
	_, err := client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: bucket, Key: key},
		Content:             strings.NewReader(content),
	})
	return FriendlyError(err)
}

// FriendlyError 将 SDK 错误转换成更易懂的中文提示（含权限建议）。
func FriendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Access Denied") || strings.Contains(msg, "AccessDenied"):
		return fmt.Errorf("TOS 访问被拒绝（Access Denied）。请确认账号对 bucket 具备相应权限（IAM 策略或桶策略），或运行 annotos check 诊断: %w", err)
	case strings.Contains(msg, "does not exist"):
		return fmt.Errorf("目标 bucket 或对象不存在，请检查 bucket 名称与 region 是否匹配: %w", err)
	case strings.Contains(msg, "InvalidAccessKeyId") || strings.Contains(msg, "SignatureDoesNotMatch"):
		return fmt.Errorf("AccessKey / SecretKey 无效或不匹配，请检查配置（annotos config 查看）: %w", err)
	default:
		return err
	}
}
