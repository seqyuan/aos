// Package db 提供 SQLite 数据库访问，用于记录 aos cp 上传任务。
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/seqyuan/aos/internal/sqldsn"
	_ "modernc.org/sqlite"
)

// DB 包装 *sql.DB。
type DB struct {
	*sql.DB
}

// Task 一条传输任务记录（上传 up / 下载 down）。
type Task struct {
	ID           int64
	Direction    string // up（上传）/ down（下载）
	SPI          string // 已废弃：仅保留用于老库表结构兼容（新任务恒为空串）
	Contract     string // 已废弃：仅保留用于老库表结构兼容（新任务恒为空串）
	LocalPath    string // up：本地源路径；down：本地落盘目录
	RemotePrefix string // up：目标前缀；down：tos 源路径
	TotalFiles   int64
	TotalBytes   int64
	DoneFiles    int64
	DoneBytes    int64
	FailedFiles  int64
	Status       string // running / done / break
	Error        string
	StartedAt    time.Time
	FinishedAt   time.Time // zero 表示未结束
	UpdatedAt    time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	direction     TEXT NOT NULL DEFAULT 'up',
	spi           TEXT NOT NULL DEFAULT '',
	contract      TEXT NOT NULL DEFAULT '',
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
);
`

// DefaultPath 返回默认任务库路径：$AOS_DB，否则 $XDG_CONFIG_HOME/aos.db，再否则 ~/.config/aos.db。
func DefaultPath() (string, error) {
	if p := os.Getenv("AOS_DB"); p != "" {
		return p, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "aos.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "aos.db"), nil
	}
	return filepath.Join(home, ".config", "aos.db"), nil
}

// Open 打开（必要时创建）数据库并初始化表结构。
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	dsn := sqldsn.File(path)
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
	// 老库迁移：补充 direction 列（历史任务视为 up）
	if err := db.ensureTaskColumn("direction"); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// ensureTaskColumn 检查 tasks 表是否存在指定列，不存在则 ALTER 添加（默认 'up'）。
func (d *DB) ensureTaskColumn(name string) error {
	rows, err := d.Query("PRAGMA table_info(tasks)")
	if err != nil {
		return fmt.Errorf("检查表结构失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var colName, ctype, dflt any
		if err := rows.Scan(&cid, &colName, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("读取表结构失败: %w", err)
		}
		if s, ok := colName.(string); ok && s == name {
			return nil
		}
	}
	if _, err := d.Exec(`ALTER TABLE tasks ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT 'up'`); err != nil {
		return fmt.Errorf("迁移表结构失败（添加 %s 列）: %w", name, err)
	}
	return nil
}

// CreateTask 创建任务记录，返回任务 ID。
func (d *DB) CreateTask(t Task) (int64, error) {
	now := time.Now()
	if t.StartedAt.IsZero() {
		t.StartedAt = now
	}
	if t.Direction == "" {
		t.Direction = "up"
	}
	res, err := d.Exec(`INSERT INTO tasks
		(direction, spi, contract, local_path, remote_prefix, total_files, total_bytes, status, started_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.Direction, t.SPI, t.Contract, t.LocalPath, t.RemotePrefix,
		t.TotalFiles, t.TotalBytes, "running",
		t.StartedAt.Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("创建任务记录失败: %w", err)
	}
	return res.LastInsertId()
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

// ListTasks 列出任务（按 ID 倒序，最多 limit 条）。
// recentOnly 为 true 时只返回：状态为 break（中断/失败）或 started_at 在 recentWindow 内的任务，
// 用于 aos stat 默认视图（避免旧的成功任务刷屏）。
const recentWindow = 48 * time.Hour

func (d *DB) ListTasks(limit int, recentOnly bool) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, direction, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, status, COALESCE(error,''),
		started_at, COALESCE(finished_at,0), updated_at FROM tasks`
	args := []any{}
	if recentOnly {
		q += ` WHERE status = 'break' OR started_at >= ?`
		args = append(args, time.Now().Add(-recentWindow).Unix())
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
		if err := rows.Scan(&t.ID, &t.Direction, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
			&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
			&t.Status, &t.Error, &start, &finish, &upd); err != nil {
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
	rows, err := d.Query(`SELECT id, direction, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, status, COALESCE(error,''),
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
	if err := rows.Scan(&t.ID, &t.Direction, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
		&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
		&t.Status, &t.Error, &start, &finish, &upd); err != nil {
		return Task{}, err
	}
	t.StartedAt = time.Unix(start, 0)
	if finish > 0 {
		t.FinishedAt = time.Unix(finish, 0)
	}
	t.UpdatedAt = time.Unix(upd, 0)
	return t, rows.Err()
}

// FindTaskByLocalPath 按 cp 上传时记录的本地路径查找最近一次上传任务（仅匹配 up，避免匹配到下载记录）。
// 依次尝试原串、Clean、Abs，以兼容相对路径与绝对路径。
func (d *DB) FindTaskByLocalPath(localPath string) (Task, error) {
	for _, c := range localPathCandidates(localPath) {
		t, ok, err := d.lookupTaskByLocalPath(c)
		if err != nil {
			return Task{}, err
		}
		if ok {
			return t, nil
		}
	}
	return Task{}, fmt.Errorf("没有找到从路径 %q 上传过的任务（该路径需与 cp 上传时的本地路径一致，或用 aos stat 查看任务）", localPath)
}

func localPathCandidates(localPath string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(localPath)
	add(filepath.Clean(localPath))
	if abs, err := filepath.Abs(localPath); err == nil {
		add(abs)
		add(filepath.Clean(abs))
	}
	return out
}

func (d *DB) lookupTaskByLocalPath(localPath string) (Task, bool, error) {
	rows, err := d.Query(`SELECT id, direction, spi, contract, local_path, remote_prefix, total_files, total_bytes,
		done_files, done_bytes, failed_files, status, COALESCE(error,''),
		started_at, COALESCE(finished_at,0), updated_at FROM tasks
		WHERE local_path=? AND direction='up' ORDER BY id DESC LIMIT 1`, localPath)
	if err != nil {
		return Task{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Task{}, false, nil
	}
	var t Task
	var start, finish, upd int64
	if err := rows.Scan(&t.ID, &t.Direction, &t.SPI, &t.Contract, &t.LocalPath, &t.RemotePrefix,
		&t.TotalFiles, &t.TotalBytes, &t.DoneFiles, &t.DoneBytes, &t.FailedFiles,
		&t.Status, &t.Error, &start, &finish, &upd); err != nil {
		return Task{}, false, err
	}
	t.StartedAt = time.Unix(start, 0)
	if finish > 0 {
		t.FinishedAt = time.Unix(finish, 0)
	}
	t.UpdatedAt = time.Unix(upd, 0)
	return t, true, rows.Err()
}

// Close 关闭数据库。
func (d *DB) Close() error {
	return d.DB.Close()
}
