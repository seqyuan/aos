package tosx

import (
	"context"
	"errors"
	"testing"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

func TestResolvePathSourceUsesPathBucketNotConfig(t *testing.T) {
	src, err := resolvePathSource("tos://other-bucket/ACME2026001/PM-x/dataset", "config-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if src.Bucket != "other-bucket" {
		t.Fatalf("Bucket = %q, want other-bucket (must not fall back to config-bucket)", src.Bucket)
	}
	if src.Prefix != "ACME2026001/PM-x/dataset/" {
		t.Fatalf("Prefix = %q", src.Prefix)
	}
}

func TestResolvePathSourceDefaultBucket(t *testing.T) {
	src, err := resolvePathSource("tos:///ACME2026001/PM-x/dataset", "config-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if src.Bucket != "config-bucket" {
		t.Fatalf("Bucket = %q, want config-bucket (tos:/// 用默认桶)", src.Bucket)
	}
	if src.Prefix != "ACME2026001/PM-x/dataset/" {
		t.Fatalf("Prefix = %q", src.Prefix)
	}
}

func TestResolvePathSourceEmptyPrefix(t *testing.T) {
	src, err := resolvePathSource("tos://solo-bucket", "config-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if src.Bucket != "solo-bucket" {
		t.Fatalf("Bucket = %q, want solo-bucket", src.Bucket)
	}
	if src.Prefix != "" {
		t.Fatalf("Prefix = %q, want empty", src.Prefix)
	}
}

func TestCollectFilesFiltersDirPlaceholders(t *testing.T) {
	objs := []tos.ListedObjectV2{
		{Key: "a/", Size: 0},
		{Key: "a/f.txt", Size: 3},
		{Key: "empty.txt", Size: 0},
	}
	files := collectFiles(objs)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	for _, f := range files {
		if f.Key == "a/" {
			t.Fatal("目录占位对象不应被保留")
		}
	}
}

func TestResolveDownloadSourceDirectoryMode(t *testing.T) {
	list := func(ctx context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error) {
		return []tos.ListedObjectV2{{Key: "C/SPI/dataset/a.txt", Size: 3}}, nil
	}
	files, prefix, single, err := resolveDownloadSource(context.Background(), list, "b", "C/SPI/dataset/")
	if err != nil {
		t.Fatal(err)
	}
	if single {
		t.Fatal("目录模式不应判定为单文件")
	}
	if prefix != "C/SPI/dataset/" {
		t.Fatalf("prefix = %q", prefix)
	}
	if len(files) != 1 || files[0].Key != "C/SPI/dataset/a.txt" {
		t.Fatal("目录文件未返回")
	}
}

// B1/B2 回归：单文件上传时对象 key 不带尾斜杠，
// 下载侧必须能从“精确对象 key”回退到单文件模式，否则永远列不到。
func TestResolveDownloadSourceFallsBackToExactKey(t *testing.T) {
	list := func(ctx context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error) {
		switch prefix {
		case "C/SPI/dataset/":
			return []tos.ListedObjectV2{{Key: "C/SPI/dataset/", Size: 0}}, nil // 仅目录占位
		case "C/SPI/dataset":
			return []tos.ListedObjectV2{{Key: "C/SPI/dataset", ETag: `"abc"`, Size: 4}}, nil
		}
		return nil, nil
	}
	files, prefix, single, err := resolveDownloadSource(context.Background(), list, "b", "C/SPI/dataset/")
	if err != nil {
		t.Fatal(err)
	}
	if !single {
		t.Fatal("应判定为单文件模式")
	}
	if prefix != "C/SPI/dataset" {
		t.Fatalf("prefix = %q, want C/SPI/dataset（无尾斜杠）", prefix)
	}
	if len(files) != 1 || files[0].Key != "C/SPI/dataset" {
		t.Fatal("精确对象未返回")
	}
}

func TestResolveDownloadSourceExactListErrorIgnored(t *testing.T) {
	list := func(ctx context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error) {
		if prefix == "C/SPI/dataset" {
			return nil, errors.New("boom")
		}
		return nil, nil
	}
	files, _, single, err := resolveDownloadSource(context.Background(), list, "b", "C/SPI/dataset/")
	if err != nil {
		t.Fatal("精确对象探测失败不应阻断下载")
	}
	if single || len(files) != 0 {
		t.Fatal("应保持空目录结果")
	}
}
