package objstore

import (
	"testing"

	"github.com/seqyuan/aos/internal/config"
)

func TestNewClientSelectsBackend(t *testing.T) {
	ossCfg := config.Config{
		Endpoint:  "oss-cn-beijing.aliyuncs.com",
		Bucket:    "b",
		AccessKey: "ak",
		SecretKey: "sk",
	}
	be, err := NewClient(ossCfg)
	if err != nil {
		t.Fatalf("NewClient(oss) 失败: %v", err)
	}
	if be.Provider() != config.ProviderOSS {
		t.Fatalf("Provider() = %q, want oss（按 endpoint 自动识别）", be.Provider())
	}

	tosCfg := config.Config{
		Endpoint:  "tos-cn-beijing.volces.com",
		Region:    "cn-beijing",
		Bucket:    "b",
		AccessKey: "ak",
		SecretKey: "sk",
	}
	be2, err := NewClient(tosCfg)
	if err != nil {
		t.Fatalf("NewClient(tos) 失败: %v", err)
	}
	if be2.Provider() != config.ProviderTOS {
		t.Fatalf("Provider() = %q, want tos", be2.Provider())
	}

	// 显式 provider 覆盖自动识别
	be3, err := NewClient(config.Config{
		Provider:  config.ProviderOSS,
		Endpoint:  "tos-cn-beijing.volces.com",
		AccessKey: "ak",
		SecretKey: "sk",
	})
	if err != nil {
		t.Fatalf("NewClient(显式 oss) 失败: %v", err)
	}
	if be3.Provider() != config.ProviderOSS {
		t.Fatalf("Provider() = %q, want oss（显式覆盖）", be3.Provider())
	}
}

func TestIsCloudPathSchemes(t *testing.T) {
	for _, s := range []string{"tos://b/x", "oss://b/x", "s3://b/x", "s3:///x"} {
		if !IsCloudPath(s) {
			t.Errorf("IsCloudPath(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"http://b/x", "https://b/x", "ftp://b/x", "b/x", "s3:/x"} {
		if IsCloudPath(s) {
			t.Errorf("IsCloudPath(%q) = true, want false", s)
		}
	}
}
