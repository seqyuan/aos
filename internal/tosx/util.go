package tosx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 上传/下载分块参数。
const (
	smallFileThreshold = 5 * 1024 * 1024 // 小于 5MB 走单次 PUT/GET
	defaultPartSize    = 20 * 1024 * 1024
	multipartTaskNum   = 4
)

func statFile(path string) (os.FileInfo, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法访问本地文件 %s: %w", path, err)
	}
	return stat, nil
}

func isNotExist(err error) bool {
	return os.IsNotExist(err)
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
