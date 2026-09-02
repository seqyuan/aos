// Package manifest 维护下载目录下的完成清单（.aos/manifest.db）。
// 用 object key + ETag 判断是否已下载完成，不依赖本地文件大小。
package manifest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seqyuan/aos/internal/sqldsn"
	_ "modernc.org/sqlite"
)

const DirName = ".aos"
const FileName = "manifest.db"

// Path 返回下载根目录下的清单路径。
func Path(localRoot string) string {
	return filepath.Join(localRoot, DirName, FileName)
}

const schema = `
CREATE TABLE IF NOT EXISTS objects (
	object_key  TEXT PRIMARY KEY,
	etag        TEXT NOT NULL,
	rel         TEXT NOT NULL,
	size        INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL
);
`

// DB 下载清单。
type DB struct {
	sql *sql.DB
	mu  sync.Mutex
}

// Object 一条已完成下载记录。
type Object struct {
	Key  string
	ETag string
	Rel  string
	Size int64
}

// Open 打开（必要时创建）下载根目录下的 .aos/manifest.db。
func Open(localRoot string) (*DB, error) {
	path := Path(localRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建清单目录失败: %w", err)
	}
	dsn := sqldsn.File(path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开下载清单失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("初始化下载清单失败: %w", err)
	}
	return &DB{sql: sqlDB}, nil
}

// Close 关闭清单库。
func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// NormalizeETag 去掉 TOS/S3 常见的引号包裹。
func NormalizeETag(etag string) string {
	return strings.Trim(strings.TrimSpace(etag), `"`)
}

// Completed 返回 object_key → 规范化 ETag。
func (d *DB) Completed() (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`SELECT object_key, etag FROM objects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, etag string
		if err := rows.Scan(&key, &etag); err != nil {
			return nil, err
		}
		out[key] = NormalizeETag(etag)
	}
	return out, rows.Err()
}

// MarkDone 记录一个对象下载成功。并发安全。
func (d *DB) MarkDone(obj Object) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO objects (object_key, etag, rel, size, finished_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(object_key) DO UPDATE SET etag=excluded.etag, rel=excluded.rel, size=excluded.size, finished_at=excluded.finished_at`,
		obj.Key, NormalizeETag(obj.ETag), obj.Rel, obj.Size, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("写入下载清单失败: %w", err)
	}
	return nil
}
