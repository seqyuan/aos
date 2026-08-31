package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindTaskByLocalPathCleanAndAbs(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	abs := filepath.Join(dir, "dataset")
	if err := os.Mkdir(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := database.CreateTask(Task{
		SPI: "PM-ACME2026001-01", Contract: "ACME2026001",
		LocalPath: abs, RemotePrefix: "ACME2026001/PM-ACME2026001-01/dataset",
		Status: "done",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := database.FindTaskByLocalPath(abs)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("id=%d want %d", got.ID, id)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got, err = database.FindTaskByLocalPath("./dataset")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("./dataset id=%d want %d", got.ID, id)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("AOS_DB", "/tmp/custom-aos.db")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/custom-aos.db" {
		t.Fatalf("got %s", p)
	}
	t.Setenv("AOS_DB", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	p, err = DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join("/tmp/xdg-config", "aos.db") {
		t.Fatalf("got %s", p)
	}
}

func TestLocalPathCandidates(t *testing.T) {
	cs := localPathCandidates("./dataset")
	if len(cs) < 2 {
		t.Fatalf("want Clean/Abs variants, got %v", cs)
	}
}
