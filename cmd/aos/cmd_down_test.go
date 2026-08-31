package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/aos/internal/db"
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
		LocalPath:    "/data/project1/dataset",
		RemotePrefix: "ACME2026001/PM-ACME2026001-01/dataset",
		TotalFiles:   2,
		LinkCount:    1,
		Status:       "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{LinkRel: "mapped.bam", LinkTarget: "sub/real.bam", ObjectKey: "ACME2026001/PM-ACME2026001-01/dataset/mapped.bam", Size: 11},
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
	restoreLinksFromTask(database, "ACME2026001/PM-ACME2026001-01/dataset", root, &buf)

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

func TestRestoreRejectsEmptyAndEscapingRel(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	id, err := database.CreateTask(db.Task{
		SPI: "PM-ACME2026001-01", Contract: "ACME2026001", LocalPath: "/d",
		RemotePrefix: "C/SPI/x", Status: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddLinks(id, []db.Link{
		{LinkRel: "", LinkTarget: "t", ObjectKey: "C/SPI/x"},
		{LinkRel: "../outside", LinkTarget: "t", ObjectKey: "C/SPI/x/../outside"},
	}); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// 预先放一个不应被删掉的文件
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	restoreLinksFromTask(database, "C/SPI/x", root, &buf)
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("空 rel 不应删掉目标目录: %v\n%s", err, buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("不安全路径")) && !bytes.Contains(buf.Bytes(), []byte("相对路径为空")) {
		t.Fatalf("应跳过空/越界路径:\n%s", buf.String())
	}
}
