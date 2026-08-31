// Package ui 提供简单的命令行进度显示。
package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/seqyuan/annotos/internal/human"
)

// Progress 并发安全的进度跟踪器。
// 终端（TTY）下显示单行实时进度；非终端下每完成一个文件打印一行。
type Progress struct {
	mu         sync.Mutex
	total      int
	done       int
	totalBytes int64
	doneBytes  int64
	failed     int
	start      time.Time
	quiet      bool
	tty        bool
	w          io.Writer
	lastLen    int
}

// NewProgress 创建进度跟踪器。
func NewProgress(total int, totalBytes int64, quiet bool, w io.Writer) *Progress {
	tty := false
	if f, ok := w.(*os.File); ok {
		if st, err := f.Stat(); err == nil {
			tty = (st.Mode() & os.ModeCharDevice) != 0
		}
	}
	return &Progress{
		total:      total,
		totalBytes: totalBytes,
		quiet:      quiet,
		tty:        tty,
		w:          w,
	}
}

// Start 开始计时。
func (p *Progress) Start() {
	p.start = time.Now()
	if !p.tty && !p.quiet && p.total > 0 {
		fmt.Fprintf(p.w, "进度: 0/%d\n", p.total)
	}
}

// Done 记录一个文件完成（size 为该文件字节数）。
func (p *Progress) Done(name string, size int64) {
	p.mu.Lock()
	p.done++
	p.doneBytes += size
	line := p.line()
	p.mu.Unlock()
	p.printLine(name, line)
}

// Fail 记录一个文件失败。
func (p *Progress) Fail(name string, err error) {
	p.mu.Lock()
	p.failed++
	p.mu.Unlock()
	if !p.quiet {
		fmt.Fprintf(p.w, "  ✗ %s: %v\n", name, err)
	}
}

// Finish 结束并打印汇总。
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && !p.quiet {
		fmt.Fprintf(p.w, "\r%s\n", padRight(p.line(), p.lastLen))
	}
	if !p.quiet {
		fmt.Fprintf(p.w, "完成: %d/%d 个文件, %s, 用时 %s",
			p.done, p.total, human.Size(p.doneBytes), time.Since(p.start).Round(time.Second))
		if p.failed > 0 {
			fmt.Fprintf(p.w, ", 失败 %d 个", p.failed)
		}
		fmt.Fprintln(p.w)
	}
}

// printLine 输出单行进度：TTY 用 \r 覆盖，非 TTY 打印一行。
func (p *Progress) printLine(name, line string) {
	if p.quiet {
		return
	}
	if p.tty {
		if len(line) > p.lastLen {
			p.lastLen = len(line)
		}
		fmt.Fprintf(p.w, "\r%s", padRight(line, p.lastLen))
		return
	}
	fmt.Fprintf(p.w, "  ✓ %s  (%s)\n", name, line)
}

// line 生成单行实时进度。
func (p *Progress) line() string {
	rate := ""
	if d := time.Since(p.start); d > 0 && p.doneBytes > 0 {
		rate = fmt.Sprintf(", %s/s", human.Size(int64(float64(p.doneBytes)/d.Seconds())))
	}
	return fmt.Sprintf("%d/%d 个文件, %s/%s%s", p.done, p.total, human.Size(p.doneBytes), human.Size(p.totalBytes), rate)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + fmt.Sprintf("%*s", n-len(s), "")
}
