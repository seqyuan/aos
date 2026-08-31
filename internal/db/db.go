// Package db 提供 SQLite 数据库访问，用于记录 annotos cp 任务及其软链接信息。
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB 包装 *sql.DB。
type DB struct {
	*sql.DB
}

// Task 一条 cp 任务记录。
type Task struct {
	ID           int64
	SPI          string
	Contract     string
	LocalPath    string
	RemotePrefix string
	TotalFiles   int64
	TotalBytes   int64
	DoneFiles    int64
	DoneBytes    int64
	FailedFiles  int64
	LinkCount    int64
	Status       string // running / done / break
	Error        string
	StartedAt    time.Time
	FinishedAt   time.Time // zero 表示未结束
	UpdatedAt    time.Time
}

// Link 一条软链接记录（对应 tasks 中的一次 cp）。
type Link struct {
	LinkRel    string // 相对上传根目录的路径，如 sub/matlink
	LinkTarget string // readlink 原值（链接指向的地址）
	ObjectKey  string // 上传后的对象 key
	Size       int64  // 文本文件字节数
}

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
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
	link_count    INTEGER NOT NULL DEFAULT 0,
	status        TEXT NOT NULL DEFAULT 'running',
	error         TEXT,
	started_at    INTEGER NOT NULL,
	finished_at   INTEGER,
	updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_spi ON tasks(spi);

CREATE TABLE IF NOT EXISTS task_links (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id     INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	link_rel    TEXT NOT NULL,
	link_target TEXT NOT NULL,
	object_key  TEXT NOT NULL,
	size        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_links_task ON task_links(task_id);
`

// DefaultPath 返回默认数据库路径：$ANNOTOS_DB 或 ~/.annotos/annotos.db。
func DefaultPath() (string, error) {
	if p := os.Getenv("ANNOTOS_DB"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "annotos.db"), nil
	}
	return filepath.Join(home, ".annotos", "annotos.db"), nil
}

// Open 打开（必要时创建）数据库并初始化表结构。
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // sqlite 单写者，串行化避免锁竞争
	db := &DB{DB: sqlDB}
	if _, err := db.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}
	return db, nil
}

// CreateTask 创建任务记录，返回任务 ID。
func (d *DB) CreateTask(t Task) (int64, error) {
	now := time.Now()
	if t.StartedAt.IsZero() {
		t.StartedAt = now
	}
	res, err := d.Exec(`INSERT INTO tasks
		(spi, contract, local_path, remote_prefix, total_files, total_bytes, link_count, status, started_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.SPI, t.Contract, t.LocalPath, t.RemotePrefix,
		t.TotalFiles, t.TotalBytes, t.LinkCount, "running",
		t.StartedAt.Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("创建任务记录失败: %w", err)
	}
	return res.LastInsertId()
}

// AddLinks 为任务批量写入软链接记录。
func (d *DB) AddLinks(taskID int64, links []Link) error {
	if len(links) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO task_links (task_id, link_rel, link_target, object_key, size) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range links {
		if _, err := stmt.Exec(taskID, l.LinkRel, l.LinkTarget, l.ObjectKey, l.Size); err != nil {
			return fmt.Errorf("写入软链接记录失败: %w", err)
		}
	}
	return tx.Commit()
}

// UpdateTaskMeta 补充任务的总量信息（任务创建时可能还不知道总量）。
func (d *DB) UpdateTaskMeta(taskID int64, remotePrefix string, totalFiles, totalBytes, linkCount int64) error {
	_, err := d.Exec(`UPDATE tasks SET remote_prefix=?, total_files=?, total_bytes=?, link_count=?, updated_at=? WHERE id=?`,
		remotePrefix, totalFiles, totalBytes, linkCount, time.Now().Unix(), taskID)
	return err
}

// UpdateProgress 更新任务进度。
func (d *DB) UpdateProgress(taskID, doneFiles, doneBytes, failedFiles int64) error {
	_, err := d.Exec(`UPDATE tasks SET done_files=?, done_bytes=?, failed_files=?, updated_at=? WHERE id=?`,
		doneFiles, doneBytes, failedFiles, time.Now().Unix(), taskID)
	return err
}

// FinishTask 结束任务：status 为 done 或 break。
func (d *DB) FinishTask(taskID int64, status, errMsg string) error {
	now := time.Now().Unix()
	_, err := d.Exec(`UPDATE tasks SET status=?, error=?, finished_at=?, updated_at=? WHERE id=?`,
		status, errMsg, now, now, taskID)
	return err
}

// ListTasks 列出任务（按 spi 过滤可选，按 ID 倒序）。
func (d *DB) ListTasks(spiFilter string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, link_count, status, COALESCE(error,''),
		started_at, COALESCE(finished_at,0), updated_at FROM tasks`
	args := []any{}
	if spiFilter != "" {
		q += ` WHERE spi = ?`
		args = append(args, spiFilter)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var start, finish, upd int64
		if err := rows.Scan(&t.ID, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
			&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
			&t.LinkCount, &t.Status, &t.Error, &start, &finish, &upd); err != nil {
			return nil, err
		}
		t.StartedAt = time.Unix(start, 0)
		if finish > 0 {
			t.FinishedAt = time.Unix(finish, 0)
		}
		t.UpdatedAt = time.Unix(upd, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask 按 ID 查询任务。
func (d *DB) GetTask(id int64) (Task, error) {
	rows, err := d.Query(`SELECT id, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, link_count, status, COALESCE(error,''),
		started_at, COALESCE(finished_at,0), updated_at FROM tasks WHERE id=?`, id)
	if err != nil {
		return Task{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Task{}, fmt.Errorf("任务 %d 不存在", id)
	}
	var t Task
	var start, finish, upd int64
	if err := rows.Scan(&t.ID, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
		&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
		&t.LinkCount, &t.Status, &t.Error, &start, &finish, &upd); err != nil {
		return Task{}, err
	}
	t.StartedAt = time.Unix(start, 0)
	if finish > 0 {
		t.FinishedAt = time.Unix(finish, 0)
	}
	t.UpdatedAt = time.Unix(upd, 0)
	return t, rows.Err()
}

// FindTaskByLocalPath 按 cp 时的 -d 本地路径查找最近一次任务（先精确匹配，再匹配绝对路径）。
func (d *DB) FindTaskByLocalPath(localPath string) (Task, error) {
	candidates := []string{localPath}
	if abs, err := filepath.Abs(localPath); err == nil && abs != localPath {
		candidates = append(candidates, abs)
	}
	for _, c := range candidates {
		rows, err := d.Query(`SELECT id, spi, contract, local_path, remote_prefix, total_files, total_bytes,
			done_files, done_bytes, failed_files, link_count, status, COALESCE(error,''),
			started_at, COALESCE(finished_at,0), updated_at FROM tasks WHERE local_path=? ORDER BY id DESC LIMIT 1`, c)
		if err != nil {
			return Task{}, err
		}
		has := rows.Next()
		if !has {
			rows.Close()
			continue
		}
		var t Task
		var start, finish, upd int64
		if err := rows.Scan(&t.ID, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
			&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
			&t.LinkCount, &t.Status, &t.Error, &start, &finish, &upd); err != nil {
			rows.Close()
			return Task{}, err
		}
		rows.Close()
		t.StartedAt = time.Unix(start, 0)
		if finish > 0 {
			t.FinishedAt = time.Unix(finish, 0)
		}
		t.UpdatedAt = time.Unix(upd, 0)
		return t, nil
	}
	return Task{}, fmt.Errorf("没有找到从路径 %q 上传过的任务（该路径需与 cp 时的 -d 一致，或用 annotos stat 查看任务）", localPath)
}

// FindTaskByRemotePrefix 按远端前缀查找最近一次任务。
func (d *DB) FindTaskByRemotePrefix(remotePrefix string) (Task, error) {
	prefix := strings.TrimSuffix(remotePrefix, "/")
	rows, err := d.Query(`SELECT id, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, link_count, status, COALESCE(error,''),
		started_at, COALESCE(finished_at,0), updated_at FROM tasks WHERE remote_prefix=? ORDER BY id DESC LIMIT 1`, prefix)
	if err != nil {
		return Task{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Task{}, fmt.Errorf("没有找到远端前缀为 %q 的任务", prefix)
	}
	var t Task
	var start, finish, upd int64
	if err := rows.Scan(&t.ID, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
		&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
		&t.LinkCount, &t.Status, &t.Error, &start, &finish, &upd); err != nil {
		return Task{}, err
	}
	t.StartedAt = time.Unix(start, 0)
	if finish > 0 {
		t.FinishedAt = time.Unix(finish, 0)
	}
	t.UpdatedAt = time.Unix(upd, 0)
	return t, nil
}

// GetLinks 查询任务的软链接记录。
func (d *DB) GetLinks(taskID int64) ([]Link, error) {
	rows, err := d.Query(`SELECT link_rel, link_target, object_key, size FROM task_links WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.LinkRel, &l.LinkTarget, &l.ObjectKey, &l.Size); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Close 关闭数据库。
func (d *DB) Close() error {
	return d.DB.Close()
}
