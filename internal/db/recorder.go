package db

import (
	"sync"
	"time"

	"github.com/seqyuan/annotos/internal/tosx"
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
	closed    bool
}

// NewRecorder 创建记录器（任务开始前调用，task 需含 SPI/Contract/LocalPath/RemotePrefix/总量）。
func NewRecorder(d *DB, task Task) (*Recorder, error) {
	id, err := d.CreateTask(task)
	if err != nil {
		return nil, err
	}
	return &Recorder{db: d, task: task, taskID: id, lastFlush: time.Now()}, nil
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
	flush := now.Sub(r.lastFlush) >= 250*time.Millisecond || r.pending >= 50
	r.mu.Unlock()
	if flush {
		r.flush()
	}
}

// Finish 结束任务。
func (r *Recorder) Finish(status, errMsg string) error {
	r.flush()
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.db.FinishTask(r.taskID, status, errMsg)
}

func (r *Recorder) flush() {
	r.mu.Lock()
	done, bytes, failed := r.done, r.bytes, r.failed
	r.lastFlush = time.Now()
	r.mu.Unlock()
	if err := r.db.UpdateProgress(r.taskID, done, bytes, failed); err != nil {
		// 进度写库失败不阻塞上传
		_ = err
	}
}

// NewTaskFromUpload 把 tosx 的统计信息组装成 db.Task。
func NewTaskFromUpload(opt tosx.UploadOptions, contract, remotePrefix string, totalFiles, totalBytes, linkCount int64) Task {
	return Task{
		SPI:          opt.SPI,
		Contract:     contract,
		LocalPath:    opt.LocalPath,
		RemotePrefix: remotePrefix,
		TotalFiles:   totalFiles,
		TotalBytes:   totalBytes,
		LinkCount:    linkCount,
		Status:       "running",
	}
}
