package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAllowsEmptyEndpointWithRegion(t *testing.T) {
	cfg := Config{Region: "cn-beijing", Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("有 region 时 endpoint 可留空（由 EndpointOrDefault 推导）: %v", err)
	}
	cfg2 := Config{Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	if err := cfg2.Validate(); err == nil {
		t.Fatal("既无 endpoint 也无 region 应报错")
	}
}

func TestValidateAuthDoesNotRequireBucket(t *testing.T) {
	// 显式 tos://bucket/... 路径时 bucket 取自路径，无需默认 bucket
	cfg := Config{Region: "cn-beijing", AccessKey: "ak", SecretKey: "sk"}
	if err := cfg.ValidateAuth(); err != nil {
		t.Fatalf("ValidateAuth 不应要求 bucket: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate 仍应要求 bucket")
	}
	cfg2 := Config{AccessKey: "ak"}
	if err := cfg2.ValidateAuth(); err == nil {
		t.Fatal("缺少 SecretKey 应报错")
	}
}

func TestEndpointOrDefaultDerivesFromRegion(t *testing.T) {
	cfg := Config{Region: "cn-beijing"}
	if got := cfg.EndpointOrDefault(); got != "tos-cn-beijing.volces.com" {
		t.Fatalf("got %q", got)
	}
	cfg2 := Config{Endpoint: "https://tos-cn-beijing.ivolces.com/"}
	if got := cfg2.EndpointOrDefault(); got != "tos-cn-beijing.ivolces.com" {
		t.Fatalf("got %q", got)
	}
}

func TestProviderOrDefault(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"显式 tos", Config{Provider: "tos", Endpoint: "oss-cn-beijing.aliyuncs.com"}, ProviderTOS},
		{"显式 oss", Config{Provider: "OSS", Endpoint: "tos-cn-beijing.volces.com"}, ProviderOSS},
		{"自动识别 oss 公网", Config{Endpoint: "oss-cn-beijing.aliyuncs.com"}, ProviderOSS},
		{"自动识别 oss 内网", Config{Endpoint: "oss-cn-beijing-internal.aliyuncs.com"}, ProviderOSS},
		{"自动识别 tos", Config{Endpoint: "tos-cn-beijing.volces.com"}, ProviderTOS},
		{"自动识别 tos 内网", Config{Endpoint: "tos-cn-beijing.ivolces.com"}, ProviderTOS},
		{"缺省 tos", Config{}, ProviderTOS},
	}
	for _, c := range cases {
		if got := c.cfg.ProviderOrDefault(); got != c.want {
			t.Errorf("%s: ProviderOrDefault() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEndpointOrDefaultOSS(t *testing.T) {
	// 显式 provider=oss + region：推导公网 OSS endpoint
	cfg := Config{Provider: ProviderOSS, Region: "cn-beijing"}
	if got := cfg.EndpointOrDefault(); got != "oss-cn-beijing.aliyuncs.com" {
		t.Fatalf("got %q", got)
	}
	// 显式 endpoint 优先
	cfg2 := Config{Endpoint: "oss-cn-beijing-internal.aliyuncs.com", Region: "cn-beijing"}
	if got := cfg2.EndpointOrDefault(); got != "oss-cn-beijing-internal.aliyuncs.com" {
		t.Fatalf("got %q", got)
	}
}

func TestScheme(t *testing.T) {
	if got := (Config{Endpoint: "oss-cn-beijing.aliyuncs.com"}).Scheme(); got != "oss" {
		t.Fatalf("Scheme() = %q, want oss", got)
	}
	if got := (Config{Endpoint: "tos-cn-beijing.volces.com"}).Scheme(); got != "tos" {
		t.Fatalf("Scheme() = %q, want tos", got)
	}
}

// Load 在配置文件不存在时返回零值（不 seed TOS 默认值），避免 aos set 把
// 默认 endpoint/region 固化进用户配置；env 覆盖仍然生效。
func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	t.Setenv("AOS_ENDPOINT", "")
	t.Setenv("AOS_REGION", "")
	t.Setenv("AOS_PROVIDER", "")
	t.Setenv("AOS_BUCKET", "")
	t.Setenv("AOS_AK", "")
	t.Setenv("AOS_SK", "")

	cfg, err := Load(filepath.Join(t.TempDir(), "not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "" || cfg.Region != "" || cfg.Provider != "" || cfg.Bucket != "" {
		t.Fatalf("文件不存在时应返回零值，got %+v", cfg)
	}
	// 运行时兜底仍应推导出可用 endpoint/后端
	if got := cfg.EndpointOrDefault(); got != DefaultEndpoint {
		t.Fatalf("EndpointOrDefault() = %q, want %q", got, DefaultEndpoint)
	}
	if got := cfg.ProviderOrDefault(); got != ProviderTOS {
		t.Fatalf("ProviderOrDefault() = %q, want tos", got)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("AOS_PROVIDER", "oss")
	t.Setenv("AOS_ENDPOINT", "oss-cn-beijing-internal.aliyuncs.com")
	t.Setenv("AOS_BUCKET", "sci-report")
	t.Setenv("AOS_AK", "ak")
	t.Setenv("AOS_SK", "sk")
	// AOS_REGION 留空避免污染

	cfg, err := Load(filepath.Join(t.TempDir(), "not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderOSS || cfg.Endpoint != "oss-cn-beijing-internal.aliyuncs.com" || cfg.Bucket != "sci-report" {
		t.Fatalf("env 覆盖未生效: %+v", cfg)
	}
}

// aos set 写入的文件应只包含用户显式设置的字段（provider 为空时省略，靠 endpoint 自动识别）。
func TestSetSavesOnlyExplicitFields(t *testing.T) {
	t.Setenv("AOS_ENDPOINT", "")
	t.Setenv("AOS_REGION", "")
	t.Setenv("AOS_PROVIDER", "")
	t.Setenv("AOS_BUCKET", "")
	t.Setenv("AOS_AK", "")
	t.Setenv("AOS_SK", "")

	path := filepath.Join(t.TempDir(), "aos.json")
	cfg := Config{Provider: ProviderOSS, Endpoint: "oss-cn-beijing-internal.aliyuncs.com", Bucket: "sci-report", AccessKey: "ak", SecretKey: "sk"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("tos-cn-beijing.volces.com")) {
		t.Fatalf("不应把 TOS 默认 endpoint 写进 OSS 配置:\n%s", data)
	}
	if bytes.Contains(data, []byte(`"region"`)) {
		t.Fatalf("未设置的 region 不应写入:\n%s", data)
	}
}
