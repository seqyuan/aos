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
