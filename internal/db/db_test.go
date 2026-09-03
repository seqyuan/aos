package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/aos/internal/sqldsn"
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

func TestOpenPathWithSpecialChars(t *testing.T) {
	dir := t.TempDir()
	// 路径含空格与 #：DSN 必须做百分号编码，否则会被当作 URI 特殊字符
	path := filepath.Join(dir, "my db#1.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer database.Close()
	id, err := database.CreateTask(Task{SPI: "PM-A-1", Contract: "A", LocalPath: "/x", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatal("id 无效")
	}
}

func TestOpenPathWithQuestionMark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a?b.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer database.Close()
	if _, err := database.CreateTask(Task{SPI: "PM-A-2", Contract: "A", LocalPath: "/x", Status: "running"}); err != nil {
		t.Fatal(err)
	}
}

// aos stat 默认视图：只显示中断/失败与近 2 天的任务；-a 显示全部。
func TestListTasksRecentOnly(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mk := func(localPath string, startedAt time.Time) int64 {
		t.Helper()
		id, err := database.CreateTask(Task{
			LocalPath: localPath, RemotePrefix: "P/" + localPath,
			StartedAt: startedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := time.Now()
	oldDone := mk("/old-done", now.Add(-10*24*time.Hour)) // 10 天前完成 → 默认不显示
	if err := database.FinishTask(oldDone, "done", ""); err != nil {
		t.Fatal(err)
	}
	oldBreak := mk("/old-break", now.Add(-10*24*time.Hour)) // 10 天前中断 → 默认显示（break 永久保留）
	if err := database.FinishTask(oldBreak, "break", "boom"); err != nil {
		t.Fatal(err)
	}
	recentDone := mk("/recent-done", now.Add(-1*time.Hour)) // 1 小时前完成 → 默认显示（2 天内）
	if err := database.FinishTask(recentDone, "done", ""); err != nil {
		t.Fatal(err)
	}
	running := mk("/running", now.Add(-1*time.Hour)) // 进行中 → 默认显示

	// 默认（recentOnly）：old-done 被过滤，其余 3 条都在
	tasks, err := database.ListTasks(20, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("recentOnly 应返回 3 条（break+近 2 天），实际 %d: %+v", len(tasks), tasks)
	}
	ids := map[int64]bool{}
	for _, t := range tasks {
		ids[t.ID] = true
	}
	for _, want := range []int64{oldBreak, recentDone, running} {
		if !ids[want] {
			t.Errorf("recentOnly 应包含任务 %d", want)
		}
	}

	// -a（全部）：4 条都在
	all, err := database.ListTasks(20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("全部任务应为 4 条，实际 %d", len(all))
	}
}

// 老库迁移：无 direction 列的表应自动补列，历史任务视为 up。
func TestMigrateAddsDirectionColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	// 模拟 v0.2.0 的旧表结构（无 direction 列）
	oldSchema := `
CREATE TABLE tasks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	spi           TEXT NOT NULL,
	contract      TEXT NOT NULL,
	local_path    TEXT NOT NULL,
	remote_prefix TEXT NOT NULL,
	total_files   INTEGER NOT NULL DEFAULT 0,
	total_bytes   INTEGER NOT NULL DEFAULT 0,
	done_files    INTEGER NOT NULL DEFAULT 0,
	done_bytes    INTEGER NOT NULL DEFAULT 0,
	failed_files  INTEGER NOT NULL DEFAULT 0,
	status        TEXT NOT NULL DEFAULT 'running',
	error         TEXT,
	started_at    INTEGER NOT NULL,
	finished_at   INTEGER,
	updated_at    INTEGER NOT NULL
);`
	raw, err := sql.Open("sqlite", sqldsn.File(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO tasks (spi,contract,local_path,remote_prefix,status,started_at,updated_at)
		VALUES ('','','/old/data','P/old/data','done',1,1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	database, err := Open(path) // 应触发迁移补列
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tasks, err := database.ListTasks(20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("任务数 = %d, want 1", len(tasks))
	}
	if tasks[0].Direction != "up" {
		t.Fatalf("历史任务 Direction = %q, want up（默认值）", tasks[0].Direction)
	}

	// 迁移后仍可正常写入 direction=down 的记录
	if _, err := database.CreateTask(Task{Direction: "down", LocalPath: "/dl", RemotePrefix: "tos://b/x"}); err != nil {
		t.Fatal(err)
	}
	all, err := database.ListTasks(20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("任务数 = %d, want 2", len(all))
	}
	if all[0].Direction != "down" {
		t.Fatalf("新任务 Direction = %q, want down", all[0].Direction)
	}
}

// FindTaskByLocalPath 只应匹配上传记录（direction=up），不能误匹配下载记录。
func TestFindTaskByLocalPathIgnoresDownloads(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.CreateTask(Task{Direction: "down", LocalPath: "/shared/path", RemotePrefix: "tos://b/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateTask(Task{Direction: "up", LocalPath: "/shared/path", RemotePrefix: "P/x"}); err != nil {
		t.Fatal(err)
	}
	got, err := database.FindTaskByLocalPath("/shared/path")
	if err != nil {
		t.Fatal(err)
	}
	if got.Direction != "up" {
		t.Fatalf("应匹配 up 任务，实际 direction=%q", got.Direction)
	}
}

// 软链接记录：AddLinks 写入后 GetTaskLinks 能按任务 ID 回读。
func TestAddAndGetTaskLinks(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	id, err := database.CreateTask(Task{LocalPath: "/data/x", RemotePrefix: "P/x"})
	if err != nil {
		t.Fatal(err)
	}
	links := []Link{
		{Rel: "sub/a.bam", Target: "/share/a.bam", ObjectKey: "P/x/sub/a.bam", Size: 13},
		{Rel: "b.txt", Target: "/share/b.txt", ObjectKey: "P/x/b.txt", Size: 12},
	}
	if err := database.AddLinks(id, links); err != nil {
		t.Fatal(err)
	}

	got, err := database.GetTaskLinks(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("GetTaskLinks = %d 条, want 2", len(got))
	}
	if got[0].Rel != "sub/a.bam" || got[0].Target != "/share/a.bam" || got[0].ObjectKey != "P/x/sub/a.bam" {
		t.Fatalf("第一条记录异常: %+v", got[0])
	}
	if got[1].Rel != "b.txt" {
		t.Fatalf("第二条记录异常: %+v", got[1])
	}

	// 其它任务不应读到这些链接
	empty, err := database.GetTaskLinks(id + 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("其它任务应读到 0 条，实际 %d", len(empty))
	}
}

// FindTaskByRemotePrefix 按远端前缀匹配最近的 up 任务（用于普通下载后还原软链接）。
func TestFindTaskByRemotePrefix(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.CreateTask(Task{Direction: "up", LocalPath: "/data/x", RemotePrefix: "tos://b/P/x"}); err != nil {
		t.Fatal(err)
	}
	// 同前缀的 down 任务不应被匹配
	if _, err := database.CreateTask(Task{Direction: "down", LocalPath: "/data/y", RemotePrefix: "tos://b/P/x"}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := database.FindTaskByRemotePrefix("tos://b/P/x")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("应匹配到 up 任务")
	}
	if got.Direction != "up" || got.LocalPath != "/data/x" {
		t.Fatalf("应匹配 up 任务 /data/x，实际 %+v", got)
	}

	// 无匹配前缀
	if _, ok, err := database.FindTaskByRemotePrefix("tos://b/other"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("不应匹配到任务")
	}
}
