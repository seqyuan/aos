package tosx

import "testing"

func TestNormalizeKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ACME2026001/PM-ACME2026001-01/dataset/a.txt", "ACME2026001/PM-ACME2026001-01/dataset/a.txt"},
		{"./abc//de/./f", "abc/de/f"},
		{"abc//de", "abc/de"},
		{"a/./b", "a/b"},
		{".//abc/de", "abc/de"},
		{"a/../b", "a/b"},
		{"../etc/passwd", "etc/passwd"},
		{"//", ""},
	}
	for _, c := range cases {
		if got := normalizeKey(c.in); got != c.want {
			t.Errorf("normalizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/real.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := makeSymlink(dir+"/real.txt", dir+"/link.txt"); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	if !isSymlink(dir + "/link.txt") {
		t.Error("link.txt 应识别为软链接")
	}
	if isSymlink(dir + "/real.txt") {
		t.Error("real.txt 不应识别为软链接")
	}
}
