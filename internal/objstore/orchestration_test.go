package objstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/aos/internal/config"
)

func testCfg() config.Config {
	return config.Config{Provider: config.ProviderTOS, Endpoint: "tos-cn-beijing.volces.com", Bucket: "b"}
}

// Upload → Download 端到端（内存后端），验证 key 铺设与内容一致。
func TestUploadDownloadRoundTripFakeBackend(t *testing.T) {
	src := t.TempDir()
	if err := writeTestFile(filepath.Join(src, "a.txt"), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(src, "sub", "b.txt"), "world"); err != nil {
		t.Fatal(err)
	}

	be := newFakeBackend()
	cfg := testCfg()
	if err := Upload(context.Background(), be, cfg, UploadOptions{
		Bucket: "b", TargetPrefix: "P/", LocalPath: src, Concurrency: 2, Quiet: true,
	}, io.Discard); err != nil {
		t.Fatalf("Upload 失败: %v", err)
	}
	for _, key := range []string{"P/a.txt", "P/sub/b.txt"} {
		if _, ok := be.objs[fakeKey("b", key)]; !ok {
			t.Fatalf("上传后缺少对象 %q，实际: %v", key, keysOf(be))
		}
	}

	dst := t.TempDir()
	if err := Download(context.Background(), be, cfg, DownloadOptions{
		Path: "tos://b/P/", LocalDir: dst, Quiet: true,
	}, io.Discard); err != nil {
		t.Fatalf("Download 失败: %v", err)
	}
	for path, want := range map[string]string{"a.txt": "hello", "sub/b.txt": "world"} {
		got, err := os.ReadFile(filepath.Join(dst, path))
		if err != nil {
			t.Fatalf("读取下载文件 %s 失败: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s 内容 = %q, want %q", path, got, want)
		}
	}
}

// 第二次带 --skip-existing 上传：内容一致，应全部跳过（不再 PutFile/PutBytes）。
func TestUploadSkipExistingFakeBackend(t *testing.T) {
	src := t.TempDir()
	if err := writeTestFile(filepath.Join(src, "a.txt"), "hello"); err != nil {
		t.Fatal(err)
	}
	be := newFakeBackend()
	cfg := testCfg()

	uploadOnce := func(skip bool) {
		t.Helper()
		if err := Upload(context.Background(), be, cfg, UploadOptions{
			Bucket: "b", TargetPrefix: "P/", LocalPath: src, Concurrency: 1, Quiet: true,
			SkipExisting: skip,
		}, io.Discard); err != nil {
			t.Fatalf("Upload(skip=%v) 失败: %v", skip, err)
		}
	}
	uploadOnce(false)
	firstPuts := be.putFileCalls + be.putBytesCall
	if firstPuts == 0 {
		t.Fatal("首次上传应至少调用一次 Put")
	}

	uploadOnce(true)
	if got := be.putFileCalls + be.putBytesCall; got != firstPuts {
		t.Fatalf("--skip-existing 应全部跳过，Put 调用数 %d -> %d", firstPuts, got)
	}

	// 内容变化后不应跳过
	if err := writeTestFile(filepath.Join(src, "a.txt"), "changed"); err != nil {
		t.Fatal(err)
	}
	uploadOnce(true)
	if got := be.putFileCalls + be.putBytesCall; got != firstPuts+1 {
		t.Fatalf("内容变化后应重新上传，Put 调用数 = %d, want %d", got, firstPuts+1)
	}
}

// 第二次下载：清单命中同 ETag + 本地存在且大小一致，应跳过（不再 GetFile）。
func TestDownloadSkipsCompletedFakeBackend(t *testing.T) {
	be := newFakeBackend()
	_ = be.PutBytes(context.Background(), "b", "P/a.txt", []byte("hello"))
	cfg := testCfg()
	dst := t.TempDir()

	download := func() {
		t.Helper()
		if err := Download(context.Background(), be, cfg, DownloadOptions{
			Path: "tos://b/P/", LocalDir: dst, Quiet: true,
		}, io.Discard); err != nil {
			t.Fatalf("Download 失败: %v", err)
		}
	}
	download()
	firstGets := be.getFileCalls
	if firstGets == 0 {
		t.Fatal("首次下载应调用 GetFile")
	}
	download()
	if be.getFileCalls != firstGets {
		t.Fatalf("第二次下载应命中清单跳过，GetFile 调用数 %d -> %d", firstGets, be.getFileCalls)
	}
}

func keysOf(be *fakeBackend) []string {
	be.mu.Lock()
	defer be.mu.Unlock()
	out := make([]string, 0, len(be.objs))
	for k := range be.objs {
		out = append(out, k)
	}
	return out
}
