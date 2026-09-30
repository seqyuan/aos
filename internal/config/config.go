// Package config 负责加载/保存 aos 的对象存储连接配置。
//
// 配置文件默认放在 aos 二进制所在目录下的 aos.json，
// 这样用户拷贝整个二进制(连同 json)到任何机器都能直接使用。
// 可用 -config 参数或环境变量 AOS_CONFIG 指定其它路径。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultFileName 默认配置文件名字（放在可执行文件同目录）。
const DefaultFileName = "aos.json"

// 默认值：火山云 TOS 北京区域。
const (
	DefaultEndpoint = "tos-cn-beijing.volces.com"
	DefaultRegion   = "cn-beijing"
)

// 支持的对象存储后端。
const (
	ProviderTOS = "tos" // 火山云 TOS
	ProviderOSS = "oss" // 阿里云 OSS
)

// Config 对象存储连接配置（支持火山云 TOS / 阿里云 OSS）。
type Config struct {
	Provider  string `json:"provider,omitempty"` // 后端：tos（默认）| oss；留空按 endpoint 自动识别
	Endpoint  string `json:"endpoint,omitempty"` // 例如 tos-cn-beijing.volces.com / oss-cn-beijing.aliyuncs.com
	Region    string `json:"region,omitempty"`   // 例如 cn-beijing
	Bucket    string `json:"bucket,omitempty"`   // 例如 example-bucket
	AccessKey string `json:"access_key_id"`      // Access Key ID
	SecretKey string `json:"secret_access_key"`  // Secret Access Key
}

// ProviderOrDefault 返回有效后端：显式配置优先，其次按 endpoint 自动识别，缺省 tos。
// 识别规则：endpoint 含 aliyuncs.com → oss；含 volces.com/ivolces.com → tos。
func (c Config) ProviderOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case ProviderTOS:
		return ProviderTOS
	case ProviderOSS:
		return ProviderOSS
	}
	ep := strings.ToLower(c.Endpoint)
	switch {
	case strings.Contains(ep, "aliyuncs.com"):
		return ProviderOSS
	case strings.Contains(ep, "volces.com"):
		return ProviderTOS
	}
	return ProviderTOS
}

// Scheme 返回云路径 scheme（与后端同名），用于路径显示与任务库远端前缀。
func (c Config) Scheme() string {
	return c.ProviderOrDefault()
}

// EndpointOrDefault 返回有效 endpoint：配置了就用配置的，否则由 region 推导。
// 推导规则：TOS → https://tos-<region>.volces.com（内网可手动配 tos-cn-beijing.ivolces.com）；
// OSS → https://oss-<region>.aliyuncs.com（内网可手动配 oss-<region>-internal.aliyuncs.com）。
func (c Config) EndpointOrDefault() string {
	if c.Endpoint != "" {
		return strings.TrimSuffix(strings.TrimPrefix(c.Endpoint, "https://"), "/")
	}
	if c.Region != "" {
		if c.ProviderOrDefault() == ProviderOSS {
			return "oss-" + c.Region + ".aliyuncs.com"
		}
		return "tos-" + c.Region + ".volces.com"
	}
	return DefaultEndpoint
}

// ResolvePath 解析配置文件路径，优先级：
//  1. 命令行 -config 参数
//  2. 环境变量 AOS_CONFIG
//  3. 可执行文件同目录下的 aos.json（用户拷贝二进制+json 即可使用）
//  4. 当前工作目录下的 aos.json（便于开发调试）
func ResolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("AOS_CONFIG"); env != "" {
		return env, nil
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), DefaultFileName))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, DefaultFileName))
	}

	var tried []string
	for _, p := range candidates {
		tried = append(tried, p)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if len(tried) == 0 {
		return "", fmt.Errorf("无法定位配置文件路径")
	}
	// 都没找到时返回第一个候选路径（exe 同目录），由调用方决定是否创建
	return tried[0], nil
}

// Load 从指定路径加载配置；文件不存在时返回零值配置（不 seed 默认值，
// 避免把 TOS 默认 endpoint/region 固化进用户配置文件；运行时兜底由
// EndpointOrDefault / ProviderOrDefault 负责）。同时允许用环境变量 AOS_* 覆盖任意字段（便于 CI 使用）。
func Load(path string) (Config, error) {
	cfg := Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(&cfg)
			return cfg, nil
		}
		return cfg, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	applyEnvOverrides(&cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("AOS_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("AOS_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("AOS_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("AOS_BUCKET"); v != "" {
		cfg.Bucket = v
	}
	if v := os.Getenv("AOS_AK"); v != "" {
		cfg.AccessKey = v
	}
	if v := os.Getenv("AOS_SK"); v != "" {
		cfg.SecretKey = v
	}
}

// Save 将配置写入 path（权限 0600，避免密钥泄露给其他用户）。
func (c Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", path, err)
	}
	return nil
}

// MaskSecret 返回脱敏后的密钥，便于安全展示。
func MaskSecret(s string) string {
	if s == "" {
		return "(未设置)"
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

// Validate 检查配置是否完整可用。
func (c Config) Validate() error {
	if err := c.ValidateAuth(); err != nil {
		return err
	}
	if c.Bucket == "" {
		return fmt.Errorf("缺少 bucket 名称")
	}
	return nil
}

// ValidateProvider 校验 provider 取值（空表示按 endpoint 自动识别）。
// 大小写不敏感，允许 "tos"/"oss"/空。
func (c Config) ValidateProvider() error {
	if p := strings.ToLower(strings.TrimSpace(c.Provider)); p != "" && p != ProviderTOS && p != ProviderOSS {
		return fmt.Errorf("provider 取值非法 %q（应为 tos 或 oss，或留空按 endpoint 自动识别）", c.Provider)
	}
	return nil
}

// ValidateAuth 校验连接必需字段（不要求 bucket）。
// 显式指定 tos://bucket/... 路径时 bucket 取自路径，无需配置默认 bucket。
func (c Config) ValidateAuth() error {
	if err := c.ValidateProvider(); err != nil {
		return err
	}
	if c.AccessKey == "" {
		return fmt.Errorf("缺少 AccessKey（access_key）")
	}
	if c.SecretKey == "" {
		return fmt.Errorf("缺少 SecretKey（secret_key）")
	}
	if c.Endpoint == "" && c.Region == "" {
		return fmt.Errorf("缺少 endpoint（endpoint 为空时需配置 region，由客户端自动推导）")
	}
	return nil
}
