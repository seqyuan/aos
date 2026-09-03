package tosx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	ok, err := SafeJoin(root, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "a", "b.txt")
	if ok != want {
		t.Fatalf("got %s want %s", ok, want)
	}

	if _, err := SafeJoin(root, ""); err == nil {
		t.Fatal("empty rel should fail")
	}
	if _, err := SafeJoin(root, "/etc/passwd"); err == nil {
		t.Fatal("absolute rel should fail")
	}
	// .. 段被丢掉后仍落在 root 内
	got, err := SafeJoin(root, "../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "etc", "passwd") {
		t.Fatalf("got %s", got)
	}
}

func TestSkipCompleted(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !skipCompleted(false, "abc", `"abc"`, f, 5) {
		t.Fatal("matching etag + dest exists + size 应跳过")
	}
	if skipCompleted(false, "abc", `"abc"`, f, 99) {
		t.Fatal("本地文件比远端小（截断）不应跳过")
	}
	if skipCompleted(true, "abc", "abc", f, 5) {
		t.Fatal("overwrite should not skip")
	}
	if skipCompleted(false, "old", "new", f, 5) {
		t.Fatal("etag change should not skip")
	}
	if skipCompleted(false, "", "abc", f, 5) {
		t.Fatal("not in manifest should not skip")
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink("/data/share/big.bam", link); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	if !skipCompleted(false, "t", "t", link, 999) {
		t.Fatal("本地为软链接时按存在即跳过（size 不适用）")
	}
	missing := filepath.Join(dir, "nope")
	if skipCompleted(false, "t", "t", missing, 5) {
		t.Fatal("missing dest should not skip even if listed")
	}
}
