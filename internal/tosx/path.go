package tosx

import (
	"fmt"
	"strings"
)

// TOSPath 解析后的 TOS 路径。
type TOSPath struct {
	Bucket string // bucket 名
	Prefix string // 对象前缀（不含开头的 '/'，末尾可能带 '/'）
}

// ParseTOSPath 解析用户输入的 TOS 路径，支持三种写法：
//
//	tos://example-bucket/ACME2026001/PM-xxx-07/matrix  显式 bucket
//	example-bucket/ACME2026001/...                    首段等于配置 bucket 时按 bucket 解析
//	ACME2026001/PM-xxx-07/matrix                  纯前缀，使用配置的默认 bucket
//
// 返回的 Prefix 统一去掉开头 '/' 并补上末尾 '/'（空前缀返回 ""）。
func ParseTOSPath(input, defaultBucket string) (TOSPath, error) {
	p := strings.TrimSpace(input)
	if p == "" {
		return TOSPath{}, fmt.Errorf("tos 路径为空")
	}

	explicit := false
	if idx := strings.Index(p, "://"); idx >= 0 {
		p = p[idx+3:]
		explicit = true
	}
	p = strings.TrimPrefix(p, "/")

	parts := strings.SplitN(p, "/", 2)
	first := parts[0]
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}

	bucket, prefix := "", ""
	switch {
	case explicit:
		// tos://bucket/prefix
		bucket, prefix = first, rest
	case first == defaultBucket && defaultBucket != "":
		// 首段就是默认 bucket
		bucket, prefix = first, rest
	default:
		// 纯前缀，使用默认 bucket
		if defaultBucket == "" {
			return TOSPath{}, fmt.Errorf("无法确定 bucket：请输入 tos://bucket/prefix 形式，或先配置默认 bucket")
		}
		bucket, prefix = defaultBucket, p
	}

	if bucket == "" {
		return TOSPath{}, fmt.Errorf("路径中缺少 bucket 名称")
	}
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return TOSPath{Bucket: bucket, Prefix: prefix}, nil
}
