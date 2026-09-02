package tosx

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 上传/下载分块参数。
const (
	smallFileThreshold = 5 * 1024 * 1024 // 小于 5MB 走单次 PUT/GET
	defaultPartSize    = 20 * 1024 * 1024
	multipartTaskNum   = 4
	minPartSize        = 5 * 1024 * 1024        // SDK 限制 5MB
	maxPartSize        = 5 * 1024 * 1024 * 1024 // SDK 限制 5GB
)

func statFile(path string) (os.FileInfo, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法访问本地文件 %s: %w", path, err)
	}
	return stat, nil
}

// ParsePartSize 解析分片大小：支持字节或带单位（KB/MB/GB，不区分大小写），
// 例如 "20MB"、"20m"、"10485760"；范围校验 5MB ~ 5GB（与 SDK 限制一致）。
func ParsePartSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("分片大小不能为空")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "GB") || strings.HasSuffix(upper, "G"):
		mult, upper = 1024*1024*1024, strings.TrimSuffix(strings.TrimSuffix(upper, "GB"), "G")
	case strings.HasSuffix(upper, "MB") || strings.HasSuffix(upper, "M"):
		mult, upper = 1024*1024, strings.TrimSuffix(strings.TrimSuffix(upper, "MB"), "M")
	case strings.HasSuffix(upper, "KB") || strings.HasSuffix(upper, "K"):
		mult, upper = 1024, strings.TrimSuffix(strings.TrimSuffix(upper, "KB"), "K")
	case strings.HasSuffix(upper, "B"):
		upper = strings.TrimSuffix(upper, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(upper), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("分片大小格式非法 %q（支持字节或 KB/MB/GB 单位，如 20MB）", s)
	}
	// 溢出防护：ParseInt 已拦截数字本身的溢出，但 n*mult 仍可能溢出 int64
	if n > 0 && n > math.MaxInt64/mult {
		return 0, fmt.Errorf("分片大小 %s 超出范围（5MB ~ 5GB）", s)
	}
	size := n * mult
	if size < minPartSize || size > maxPartSize {
		return 0, fmt.Errorf("分片大小 %s 超出范围（5MB ~ 5GB）", s)
	}
	return size, nil
}

// partSizeOrDefault 分片大小未指定时用默认值。
func partSizeOrDefault(ps int64) int64 {
	if ps <= 0 {
		return defaultPartSize
	}
	return ps
}

// taskNumOrDefault 单文件分片并发未指定时用默认值。
func taskNumOrDefault(n int) int {
	if n <= 0 {
		return multipartTaskNum
	}
	return n
}

// ExcludeMatch 判断 relPath 或 basename 是否命中任一排除规则（支持 * ? 通配）。
// 规则还会匹配 relPath 的每一级祖先路径，例如规则 "data" 会排除 data/ 下所有内容。
func ExcludeMatch(relPath, name string, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		// 匹配完整相对路径
		if ok, _ := filepath.Match(r, relPath); ok {
			return true
		}
		// 匹配 basename
		if ok, _ := filepath.Match(r, name); ok {
			return true
		}
		// 匹配每一级祖先路径（排除目录本身及其下所有内容）
		for _, anc := range ancestors(relPath) {
			if ok, _ := filepath.Match(r, anc); ok {
				return true
			}
		}
	}
	return false
}

// ancestors 返回 relPath 的所有祖先前缀，例如 a/b/c -> [a, a/b]。
func ancestors(relPath string) []string {
	parts := strings.Split(relPath, "/")
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}
