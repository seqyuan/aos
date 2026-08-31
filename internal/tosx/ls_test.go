package tosx

import (
	"bytes"
	"testing"
	"time"
)

func TestParseTOSPath(t *testing.T) {
	cases := []struct {
		in      string
		bucket  string
		prefix  string
		wantErr bool
	}{
		{"tos://example-bucket/ACME2026001/PM-x/dataset", "example-bucket", "ACME2026001/PM-x/dataset/", false},
		{"tos://example-bucket", "example-bucket", "", false},
		{"example-bucket/ACME2026001", "example-bucket", "ACME2026001/", false},
		{"ACME2026001/PM-x/dataset", "example-bucket", "ACME2026001/PM-x/dataset/", false},
		{"tos://example-bucket/ACME2026001//dataset", "example-bucket", "ACME2026001/dataset/", false},
		{"", "", "", true},
	}
	for _, c := range cases {
		got, err := ParseTOSPath(c.in, "example-bucket")
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseTOSPath(%q): want error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTOSPath(%q): %v", c.in, err)
			continue
		}
		if got.Bucket != c.bucket || got.Prefix != c.prefix {
			t.Errorf("ParseTOSPath(%q) = %+v, want bucket=%q prefix=%q", c.in, got, c.bucket, c.prefix)
		}
	}
}

func TestExcludeMatch(t *testing.T) {
	cases := []struct {
		rel   string
		name  string
		rules []string
		want  bool
	}{
		{"a/tmp.log", "tmp.log", []string{"*.log"}, true},
		{"a/tmp.log", "tmp.log", []string{"*.tmp"}, false},
		{".git/config", "config", []string{".git"}, true},
		{"data/x", "x", []string{"data"}, true},
	}
	for _, c := range cases {
		if got := ExcludeMatch(c.rel, c.name, c.rules); got != c.want {
			t.Errorf("ExcludeMatch(%q,%q,%v) = %v, want %v", c.rel, c.name, c.rules, got, c.want)
		}
	}
}

func TestTreeOutput(t *testing.T) {
	root := newDir("matrix")
	root.ensureDir("sub").children["b.txt"] = &treeNode{name: "b.txt", size: 20, children: map[string]*treeNode{}}
	root.children["a.txt"] = &treeNode{name: "a.txt", size: 10, mod: time.Now(), children: map[string]*treeNode{}}
	root.children["z.bin"] = &treeNode{name: "z.bin", size: 5 * 1024 * 1024, children: map[string]*treeNode{}}

	var buf bytes.Buffer
	printNode(&buf, root, "", true, 0, 0, false)
	out := buf.String()
	for _, want := range []string{"sub/", "a.txt  (10B)", "z.bin  (5.0MB)"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("tree output missing %q:\n%s", want, out)
		}
	}
	// 目录应排在文件前（sub 目录先于 a.txt 出现）
	subIdx := bytes.Index(buf.Bytes(), []byte("├── sub/"))
	aIdx := bytes.Index(buf.Bytes(), []byte("├── a.txt"))
	if subIdx < 0 || aIdx < 0 || subIdx > aIdx {
		t.Errorf("dirs should come first:\n%s", out)
	}
}
