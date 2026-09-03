package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/aos/internal/db"
)

// restoreSymlinks：下载完成后按上传记录的软链接明细，把文本文件还原为 symlink。
func TestRestoreSymlinks(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "aos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	id, err := database.CreateTask(db.Task{Direction: "up", LocalPath: dir, RemotePrefix: "P/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{Rel: "sub/link.bam", Target: "/data/share/big.bam", ObjectKey: "P/x/sub/link.bam", Size: 18},
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟已下载的文本文件（内容为链接目标地址）
	linkPath := filepath.Join(dir, "sub", "link.bam")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte("/data/share/big.bam"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreSymlinks(database, id, dir); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("文本文件应被还原为软链接: %v", err)
	}
	if target != "/data/share/big.bam" {
		t.Fatalf("link target = %q, want /data/share/big.bam", target)
	}
}

// 顶层链接（Rel 为空）：还原时直接在 localDir 上建链接（单文件上传场景，localDir 即源文件路径）。
func TestRestoreSymlinksTopLevel(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "aos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	id, err := database.CreateTask(db.Task{Direction: "up", LocalPath: dir, RemotePrefix: "P/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{Rel: "", Target: "/data/share/big.bam", ObjectKey: "P/x", Size: 18},
	}); err != nil {
		t.Fatal(err)
	}

	// 单文件上传场景：localDir 即源文件路径（已下载的文本文件）
	srcFile := filepath.Join(dir, "mylink")
	if err := os.WriteFile(srcFile, []byte("/data/share/big.bam"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restoreSymlinks(database, id, srcFile); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(srcFile)
	if err != nil {
		t.Fatalf("顶层链接应被还原: %v", err)
	}
	if target != "/data/share/big.bam" {
		t.Fatalf("link target = %q", target)
	}
}

// 普通下载：restoreLinksAfterDownload 按远端前缀匹配 up 任务并还原软链接。
func TestRestoreLinksAfterDownloadNormal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "aos.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// up 任务记录完整 tos:// 远端前缀 + 软链接明细
	id, err := database.CreateTask(db.Task{Direction: "up", LocalPath: "/data/x", RemotePrefix: "tos://b/P/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(id, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{Rel: "sub/link.bam", Target: "/share/big.bam", ObjectKey: "P/x/sub/link.bam", Size: 13},
	}); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// 模拟已下载的文本文件
	linkPath := filepath.Join(dir, "sub", "link.bam")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte("/share/big.bam"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreLinksAfterDownload(dbPath, nil, db.Task{}, false, "tos://b/P/x", dir, "b"); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("文本文件应被还原为软链接: %v", err)
	}
	if target != "/share/big.bam" {
		t.Fatalf("link target = %q, want /share/big.bam", target)
	}
}

// Rel 含 .. 或绝对路径时不得改写下载目录之外的文件。
func TestRestoreSymlinksDoesNotEscapeDownloadDir(t *testing.T) {
	parent := t.TempDir()
	localDir := filepath.Join(parent, "dl")
	if err := os.Mkdir(localDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("relative-dotdot", func(t *testing.T) {
		outside := filepath.Join(parent, "secret.txt")
		if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		database, err := db.Open(filepath.Join(t.TempDir(), "aos.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		id, err := database.CreateTask(db.Task{Direction: "up", LocalPath: localDir, RemotePrefix: "P/x"})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.AddLinks(id, []db.Link{
			{Rel: "../secret.txt", Target: "/share/x", ObjectKey: "P/x/secret.txt", Size: 8},
		}); err != nil {
			t.Fatal(err)
		}
		_ = restoreSymlinks(database, id, localDir)
		got, err := os.ReadFile(outside)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "keep" {
			t.Fatalf("下载目录外的文件被改写: %q", got)
		}
	})

	t.Run("absolute", func(t *testing.T) {
		outside := filepath.Join(parent, "abs.txt")
		if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		database, err := db.Open(filepath.Join(t.TempDir(), "aos.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		id, err := database.CreateTask(db.Task{Direction: "up", LocalPath: localDir, RemotePrefix: "P/x"})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.AddLinks(id, []db.Link{
			{Rel: outside, Target: "/share/x", ObjectKey: "P/x/abs.txt", Size: 8},
		}); err != nil {
			t.Fatal(err)
		}
		if err := restoreSymlinks(database, id, localDir); err == nil {
			t.Fatal("绝对路径 Rel 应拒绝")
		}
		got, err := os.ReadFile(outside)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "keep" {
			t.Fatalf("绝对路径 Rel 改写了目录外文件: %q", got)
		}
	})
}

func TestReplaceWithSymlinkReplacesFileAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceWithSymlink(f, "/data/share/big.bam"); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(f)
	if err != nil {
		t.Fatalf("应成为软链接: %v", err)
	}
	if got != "/data/share/big.bam" {
		t.Fatalf("target = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".aos-link") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

func TestRestoreLinksAfterDownloadWarnsOnBadPath(t *testing.T) {
	out := captureStderr(t, func() {
		if err := restoreLinksAfterDownload("", nil, db.Task{}, false, "", t.TempDir(), "b"); err != nil {
			t.Errorf("解析失败应跳过而非失败: %v", err)
		}
	})
	if !strings.Contains(out, "跳过还原软链接") {
		t.Fatalf("无法解析远端路径时应提示跳过还原: %q", out)
	}
}

func TestRestoreLinksAfterDownloadWarnsOnDBOpenError(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(notDir, "aos.db")
	out := captureStderr(t, func() {
		if err := restoreLinksAfterDownload(dbPath, nil, db.Task{}, false, "tos://b/P/x", t.TempDir(), "b"); err != nil {
			t.Errorf("打开库失败应跳过而非失败: %v", err)
		}
	})
	if !strings.Contains(out, "跳过还原软链接") {
		t.Fatalf("无法打开任务库时应提示跳过还原: %q", out)
	}
}

func TestRestoreLinksAfterDownloadSilentWhenNoUpTask(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "aos.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	out := captureStderr(t, func() {
		if err := restoreLinksAfterDownload(dbPath, nil, db.Task{}, false, "tos://b/P/x", dir, "b"); err != nil {
			t.Errorf("无匹配任务应跳过: %v", err)
		}
	})
	if strings.Contains(out, "跳过还原软链接") {
		t.Fatalf("无匹配 up 任务不应提示: %q", out)
	}
}
