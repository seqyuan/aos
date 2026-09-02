package db

import (
	"sync"
	"time"
)

// Recorder 实现 tosx.UploadRecorder，把 up 任务进度写入 SQLite。
// 进度写入节流（250ms 或 50 条），避免频繁更新数据库。
type Recorder struct {
	db     *DB
	task   Task
	taskID int64

	mu        sync.Mutex
	flushMu   sync.Mutex // 单飞：同一时刻仅一个 goroutine 执行 flush，避免并发 UpdateProgress 乱序
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
func (r *Recorder) Begin(remotePrefix string, totalFiles, totalBytes int64) (int64, error) {
	r.task.RemotePrefix = remotePrefix
	r.task.TotalFiles = totalFiles
	r.task.TotalBytes = totalBytes
	id, err := r.db.CreateTask(r.task)
	if err != nil {
		return 0, err
	}
	r.taskID = id
	return id, nil
}

// TaskID 返回任务 ID。
func (r *Recorder) TaskID() int64 { return r.taskID }

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
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	for {
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
		if !again {
			return
		}
	}
}
