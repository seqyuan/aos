package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkDoneAndCompleted(t *testing.T) {
	root := t.TempDir()
	db, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := os.Stat(Path(root)); err != nil {
		t.Fatalf("expected %s: %v", Path(root), err)
	}

	if err := db.MarkDone(Object{Key: "a/b.txt", ETag: `"abc"`, Rel: "b.txt", Size: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Completed()
	if err != nil {
		t.Fatal(err)
	}
	if got["a/b.txt"] != "abc" {
		t.Fatalf("etag=%q want abc", got["a/b.txt"])
	}

	if err := db.MarkDone(Object{Key: "a/b.txt", ETag: "def", Rel: "b.txt", Size: 4}); err != nil {
		t.Fatal(err)
	}
	got, err = db.Completed()
	if err != nil {
		t.Fatal(err)
	}
	if got["a/b.txt"] != "def" {
		t.Fatalf("updated etag=%q", got["a/b.txt"])
	}
}

func TestPath(t *testing.T) {
	got := Path("/data/dataset")
	want := filepath.Join("/data/dataset", ".aos", "manifest.db")
	if got != want {
		t.Fatalf("%s != %s", got, want)
	}
}

func TestNormalizeETag(t *testing.T) {
	if NormalizeETag(`"xyz"`) != "xyz" {
		t.Fatal("quotes")
	}
	if NormalizeETag("xyz") != "xyz" {
		t.Fatal("plain")
	}
}
