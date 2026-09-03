package main

import (
	"os"
	"path/filepath"
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
