package objstore

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 支持的云路径 scheme（统一按云路径处理，后端由配置的 provider 决定）。
var cloudSchemes = []string{"tos", "oss", "s3"}

// CloudScheme 判断输入是否为受支持的云路径 scheme 前缀，返回 scheme 名。
func CloudScheme(input string) (string, bool) {
	idx := strings.Index(input, "://")
	if idx < 0 {
		return "", false
	}
	scheme := strings.ToLower(strings.TrimSpace(input[:idx]))
	for _, s := range cloudSchemes {
		if scheme == s {
			return scheme, true
		}
	}
	return "", false
}

// IsCloudPath 判断是否为云上路径（tos:// / oss:// / s3://）。
func IsCloudPath(s string) bool {
	_, ok := CloudScheme(s)
	return ok
}

// CloudPath 解析后的云存储路径。
type CloudPath struct {
	Scheme string // 输入显式 scheme；空表示未指定（沿用配置后端 scheme）
	Bucket string // bucket 名
	Prefix string // 对象前缀（不含开头的 '/'，末尾可能带 '/'）
}

// ParseCloudPath 解析用户输入的云路径，支持三种 scheme（tos/oss/s3）与以下写法：
//
//	tos://example-bucket/ACME2026001/dataset   显式 bucket
//	oss:///ACME2026001/dataset                 bucket 用配置默认值
//	example-bucket/ACME2026001/...             首段等于配置 bucket 时按 bucket 解析
//	ACME2026001/dataset                        纯前缀，使用配置的默认 bucket
//
// 返回的 Prefix 统一去掉开头 '/' 并补上末尾 '/'（空前缀返回 ""）。
func ParseCloudPath(input, defaultBucket string) (CloudPath, error) {
	p := strings.TrimSpace(input)
	if p == "" {
		return CloudPath{}, fmt.Errorf("云路径为空")
	}

	scheme := ""
	explicit := false
	if s, ok := CloudScheme(p); ok {
		scheme = s
		p = p[strings.Index(p, "://")+3:]
		explicit = true
		if strings.HasPrefix(p, "/") {
			// scheme:///prefix：bucket 用配置默认值
			p = strings.TrimPrefix(p, "/")
			explicit = false
		}
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
		// scheme://bucket/prefix
		bucket, prefix = first, rest
	case first == defaultBucket && defaultBucket != "":
		// 首段就是默认 bucket
		bucket, prefix = first, rest
	default:
		// 纯前缀，使用默认 bucket
		if defaultBucket == "" {
			return CloudPath{}, fmt.Errorf("无法确定 bucket：请输入 tos://bucket/prefix（或 oss://、s3://）形式，或先配置默认 bucket")
		}
		bucket, prefix = defaultBucket, p
	}

	if bucket == "" {
		return CloudPath{}, fmt.Errorf("路径中缺少 bucket 名称")
	}
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix != "" {
		prefix = normalizeKey(strings.TrimSuffix(prefix, "/"))
		if prefix != "" {
			prefix += "/"
		}
	}
	return CloudPath{Scheme: scheme, Bucket: bucket, Prefix: prefix}, nil
}

// SafeJoin 把 slash 分隔的相对路径拼到 root 下。
// 拒绝空路径、绝对路径，以及规范化后仍逃出 root 的路径。
func SafeJoin(root, rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || rel == "." {
		return "", fmt.Errorf("相对路径为空")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("不允许绝对路径: %s", rel)
	}
	cleanRel := normalizeKey(rel)
	if cleanRel == "" {
		return "", fmt.Errorf("相对路径为空")
	}
	dest := filepath.Join(root, filepath.FromSlash(cleanRel))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	back, err := filepath.Rel(absRoot, absDest)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越界: %s", rel)
	}
	return dest, nil
}
