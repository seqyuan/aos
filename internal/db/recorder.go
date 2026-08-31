package db

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/seqyuan/aos/internal/tosx"
)

// Recorder 实现 tosx.UploadRecorder，把 cp 任务进度写入 SQLite。
// 进度写入节流（250ms 或 50 条），避免频繁更新数据库。
type Recorder struct {
	db     *DB
	task   Task
	taskID int64

	mu        sync.Mutex
	done      int64
	bytes     int64
	failed    int64
	lastFlush time.Time
	pending   int
	flushing  bool
	closed    bool
}

// NewRecorder 创建记录器。任务行在 Begin 时才插入（walk 完成、总量已知之后）。
func NewRecorder(d *DB, task Task) *Recorder {
	return &Recorder{db: d, task: task, lastFlush: time.Now()}
}

// Begin 在总量已知后插入任务行。
func (r *Recorder) Begin(remotePrefix string, totalFiles, totalBytes, linkCount int64) (int64, error) {
	r.task.RemotePrefix = remotePrefix
	r.task.TotalFiles = totalFiles
	r.task.TotalBytes = totalBytes
	r.task.LinkCount = linkCount
	id, err := r.db.CreateTask(r.task)
	if err != nil {
		return 0, err
	}
	r.taskID = id
	return id, nil
}

// TaskID 返回任务 ID。
func (r *Recorder) TaskID() int64 { return r.taskID }

// AddLinks 记录软链接。
func (r *Recorder) AddLinks(links []Link) error { return r.db.AddLinks(r.taskID, links) }

// Progress 累加进度并节流刷新。
func (r *Recorder) Progress(doneFiles, doneBytes, failedFiles int64) {
	r.mu.Lock()
	r.done += doneFiles
	r.bytes += doneBytes
	r.failed += failedFiles
	r.pending++
	now := time.Now()
	flush := !r.flushing && (now.Sub(r.lastFlush) >= 250*time.Millisecond || r.pending >= 50)
	if flush {
		r.flushing = true
	}
	r.mu.Unlock()
	if flush {
		r.flush()
	}
}

// Finish 结束任务。
func (r *Recorder) Finish(status, errMsg string) error {
	if r.taskID == 0 {
		return nil
	}
	r.flush()
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.db.FinishTask(r.taskID, status, errMsg)
}

func (r *Recorder) flush() {
	r.mu.Lock()
	if r.taskID == 0 {
		r.flushing = false
		r.mu.Unlock()
		return
	}
	done, bytes, failed := r.done, r.bytes, r.failed
	r.lastFlush = time.Now()
	r.pending = 0
	r.mu.Unlock()
	if err := r.db.UpdateProgress(r.taskID, done, bytes, failed); err != nil {
		_ = err
	}
	r.mu.Lock()
	r.flushing = false
	again := r.pending > 0 && !r.closed
	r.mu.Unlock()
	if again {
		r.flush()
	}
}

// NewTaskFromUpload 把 tosx 的统计信息组装成 db.Task。
func NewTaskFromUpload(opt tosx.UploadOptions, contract, remotePrefix string, totalFiles, totalBytes, linkCount int64) Task {
	return Task{
		SPI:          opt.SPI,
		Contract:     contract,
		LocalPath:    filepath.Clean(opt.LocalPath),
		RemotePrefix: remotePrefix,
		TotalFiles:   totalFiles,
		TotalBytes:   totalBytes,
		LinkCount:    linkCount,
		Status:       "running",
	}
}
