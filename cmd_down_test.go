package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/annotos/internal/db"
)

func TestRestoreLinksFromTask(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// 造一条任务 + 软链接记录
	id, err := database.CreateTask(db.Task{
		SPI:          "PM-ACME2026001-01",
		Contract:     "ACME2026001",
		LocalPath:    "/data/project1/matrix",
		RemotePrefix: "ACME2026001/PM-ACME2026001-01/matrix",
		TotalFiles:   2,
		LinkCount:    1,
		Status:       "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{LinkRel: "mapped.bam", LinkTarget: "sub/real.bam", ObjectKey: "ACME2026001/PM-ACME2026001-01/matrix/mapped.bam", Size: 11},
	}); err != nil {
		t.Fatal(err)
	}

	// 本地放一个内容=链接目标的文本文件（模拟下载回来的软链接文本）
	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	linkFile := filepath.Join(root, "mapped.bam")
	if err := os.WriteFile(linkFile, []byte("sub/real.bam"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	restoreLinksFromTask(database, "ACME2026001/PM-ACME2026001-01/matrix", root, &buf)

	// 断言：mapped.bam 已变为指向 sub/real.bam 的软链接
	fi, err := os.Lstat(linkFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mapped.bam 应还原为软链接，当前 mode=%v\n输出:\n%s", fi.Mode(), buf.String())
	}
	target, err := os.Readlink(linkFile)
	if err != nil {
		t.Fatal(err)
	}
	if target != "sub/real.bam" {
		t.Fatalf("链接目标 = %q, want sub/real.bam", target)
	}
}

func TestRestoreSkipsMismatchedContent(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	id, err := database.CreateTask(db.Task{
		SPI:          "PM-ACME2026001-01",
		Contract:     "ACME2026001",
		LocalPath:    "/d",
		RemotePrefix: "C/SPI/x",
		Status:       "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{LinkRel: "a", LinkTarget: "targetA", ObjectKey: "C/SPI/x/a"},
	}); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "a")
	// 内容与记录不一致（比如是真实文件）
	if err := os.WriteFile(f, []byte("DIFFERENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	restoreLinksFromTask(database, "C/SPI/x", root, &buf)
	if !bytes.Contains(buf.Bytes(), []byte("内容与记录不一致")) {
		t.Fatalf("应提示内容不一致:\n%s", buf.String())
	}
	// 文件应保持原样（普通文件）
	fi, _ := os.Lstat(f)
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("内容不一致时不应转成软链接")
	}
}
