package config

import "testing"

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
