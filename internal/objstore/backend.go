// Package objstore 抽象对象存储后端（火山云 TOS / 阿里云 OSS），
// 并提供 aos 需要的上传/下载/列举/删除编排与云路径解析。
//
// 后端通过 Backend 接口解耦：命令行与编排层只依赖接口，
// 具体 SDK（TOS / OSS）在各自 backend 实现里封装。
package objstore

import (
	"context"
	"fmt"
	"hash/crc64"
	"strings"
	"time"

	"github.com/seqyuan/aos/internal/config"
)

// crc64Table 全局 CRC64-ECMA 表（TOS 与 OSS 服务端均使用该多项式）。
var crc64Table = crc64.MakeTable(crc64.ECMA)

// Object 对象存储中的对象元信息（跨后端统一）。
// CRC64 为 0 表示后端未提供（如 OSS 的 List），调用方可按需 Stat 补齐。
type Object struct {
	Key          string
	Size         int64
	ETag         string
	CRC64        uint64
	LastModified time.Time
}

// MultipartUpload 未完成的分片上传任务。
type MultipartUpload struct {
	Key      string
	UploadID string
}

// TransferOpt 大文件分片传输参数。
type TransferOpt struct {
	PartSize      int64  // 分片大小（0 用默认 20MB）
	TaskNum       int    // 单文件分片并发（0 用默认 4）
	CheckpointDir string // 非空则开启分片断点续传（checkpoint 存于该目录）
}

// Backend 对象存储后端接口。
//
// 设计约定：
//   - ListPage 是唯一的列举原语，ListAll 基于它实现（见下方自由函数）；
//   - Fingerprints 是唯一的“内容指纹”入口，后端自行选择最优路径
//     （TOS 从 List 直接拿到 crc64；OSS List 后再并发 HEAD 补齐），
//     编排层不关心后端能力差异。
//
// 取消语义：所有方法都应尊重 ctx；OSS 受 SDK 限制，分片上传的 part/complete
// 请求无法取消（在途请求需等 SDK 超时）。
type Backend interface {
	// ListPage 列出 bucket 下 prefix 前缀的对象，最多 maxKeys 个，从 token 开始。
	// 返回本页对象与下一页 token（无下一页时为空串）。
	ListPage(ctx context.Context, bucket, prefix string, maxKeys int, token string) (objs []Object, nextToken string, err error)
	// Fingerprints 返回 keys 中确实存在于 bucket/prefix 下对象的内容指纹。
	// 不存在的 key 不出现在结果中；crc64 为 0 表示后端无法提供。
	Fingerprints(ctx context.Context, bucket, prefix string, keys []string) (map[string]Fingerprint, error)
	// PutFile 上传单个文件（大文件自动分片）。
	PutFile(ctx context.Context, bucket, key, localPath string, opt TransferOpt) error
	// PutBytes 上传一段内存内容（用于软链接转文本文件）。
	PutBytes(ctx context.Context, bucket, key string, data []byte) error
	// GetFile 下载单个对象到本地文件；size 为远端对象大小（用于选择单次/分片）。
	GetFile(ctx context.Context, bucket, key, localPath string, size int64, opt TransferOpt) error
	// DeleteObject 删除单个对象（幂等）。
	DeleteObject(ctx context.Context, bucket, key string) error
	// DeleteObjects 批量删除；返回删除失败的对象 key（删除不存在的对象视为成功）。
	DeleteObjects(ctx context.Context, bucket string, keys []string) (failed []string, err error)
	// ListUploads 列出指定前缀下未完成的分片上传任务。
	ListUploads(ctx context.Context, bucket, prefix string) ([]MultipartUpload, error)
	// AbortUpload 取消一个未完成的分片上传任务。
	AbortUpload(ctx context.Context, bucket, key, uploadID string) error
}

// Fingerprint 对象内容指纹：大小 + CRC64-ECMA（0 表示后端无法提供）。
type Fingerprint struct {
	Size  int64
	CRC64 uint64
}

// ListAll 通过后端的 ListPage 分页列出 bucket 下 prefix 前缀的所有对象。
func ListAll(ctx context.Context, be Backend, bucket, prefix string) ([]Object, error) {
	var all []Object
	token := ""
	for {
		objs, next, err := be.ListPage(ctx, bucket, prefix, listPageSize, token)
		if err != nil {
			return nil, err
		}
		all = append(all, objs...)
		if next == "" {
			break
		}
		token = next
	}
	return all, nil
}

// listPageSize 分页列举单页大小（TOS/OSS 上限均为 1000）。
const listPageSize = 1000

// NewClient 根据配置创建对应后端的客户端。
// 后端由 cfg.ProviderOrDefault() 决定（显式 provider 优先，否则按 endpoint 识别）。
func NewClient(cfg config.Config) (Backend, error) {
	switch cfg.ProviderOrDefault() {
	case config.ProviderOSS:
		return newOSSBackend(cfg)
	default:
		return newTOSBackend(cfg)
	}
}

// FriendlyError 将 SDK 错误转换成更易懂的中文提示（含权限建议），
// 兼容 TOS 的文本错误与 OSS 的 ServiceError.Code。
func FriendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Access Denied") || strings.Contains(msg, "AccessDenied"):
		return fmt.Errorf("对象存储访问被拒绝（Access Denied）。请确认账号对 bucket 具备相应权限（IAM 策略或桶策略），或运行 aos check 诊断: %w", err)
	case strings.Contains(msg, "does not exist") || strings.Contains(msg, "NoSuchBucket") || strings.Contains(msg, "NoSuchKey"):
		return fmt.Errorf("目标 bucket 或对象不存在，请检查 bucket 名称与 region 是否匹配: %w", err)
	case strings.Contains(msg, "InvalidAccessKeyId") || strings.Contains(msg, "SignatureDoesNotMatch"):
		return fmt.Errorf("AccessKey / SecretKey 无效或不匹配，请检查配置（aos config 查看）: %w", err)
	default:
		return err
	}
}
