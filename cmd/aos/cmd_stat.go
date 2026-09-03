package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/seqyuan/aos/internal/db"
	"github.com/seqyuan/aos/internal/human"
	"github.com/spf13/pflag"
)

// cmdStat aos stat：查询传输历史（上传/下载均记录；默认只看中断/失败与近 2 天的任务，-a 显示全部）。
func cmdStat(args []string) int {
	fs := pflag.NewFlagSet("aos stat", pflag.ContinueOnError)
	all := fs.BoolP("all", "a", false, "显示全部任务（默认只显示中断/失败与近 2 天的任务）")
	taskID := fs.Int64("id", 0, "查看指定任务详情")
	limit := fs.Int("limit", 20, "最多列出多少条")
	dbPath := fs.String("db", "", "sqlite 数据库路径（默认 ~/.config/aos.db）")
	if ok, err := parseFlagSet(fs, args, "用法: aos stat [选项]\n\n示例:\n  aos stat            # 中断/失败 + 近 2 天的任务\n  aos stat -a         # 全部任务\n  aos stat --id 3     # 某次任务详情（错误信息等）"); !ok {
		return 2
	} else if err != nil {
		return 2
	}

	path := *dbPath
	if path == "" {
		if p, err := db.DefaultPath(); err == nil {
			path = p
		}
	}
	database, err := db.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: 无法打开数据库 %s: %v\n", path, err)
		return 1
	}
	defer database.Close()

	if *taskID > 0 {
		return statDetail(database, path, *taskID)
	}
	return statList(database, path, *limit, *all)
}

func statList(database *db.DB, dbPath string, limit int, all bool) int {
	tasks, err := database.ListTasks(limit, !all)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		if all {
			fmt.Printf("还没有任务记录（数据库 %s）\n", dbPath)
		} else {
			fmt.Printf("近 2 天没有进行中/中断的任务，也没有失败任务（数据库 %s；aos stat -a 查看全部）\n", dbPath)
		}
		return 0
	}
	if !all {
		fmt.Printf("（仅显示中断/失败与近 2 天的任务；aos stat -a 查看全部）\n")
	}
	// 列宽按显示宽度对齐（全角字符占 2 列），表头与数据逐列左对齐，列间一个空格分隔
	header := strings.Join([]string{
		padDisplay("ID", 4), padDisplay("方向", 4), padDisplay("状态", 8),
		padDisplay("文件进度", 17), padDisplay("开始时间", 13), padDisplay("完成/错误", 13), "路径",
	}, " ")
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", 104))
	for _, t := range tasks {
		status := t.Status
		if t.Status == "running" && time.Since(t.UpdatedAt) > 30*time.Second {
			status = "running?"
		}
		direction := t.Direction
		if direction == "" {
			direction = "up"
		}
		// 路径：上传显示本地路径，下载显示 tos 源路径
		path := t.LocalPath
		if direction == "down" {
			path = t.RemotePrefix
		}
		fileProgress := fmt.Sprintf("%d/%d", t.DoneFiles, t.TotalFiles)
		if t.FailedFiles > 0 {
			fileProgress += fmt.Sprintf("(失败%d)", t.FailedFiles)
		}
		start := t.StartedAt.Format("01-02 15:04")
		end := ""
		if !t.FinishedAt.IsZero() {
			end = t.FinishedAt.Format("01-02 15:04")
		} else {
			end = statusLine(t)
		}
		fmt.Println(strings.Join([]string{
			padDisplay(fmt.Sprintf("%d", t.ID), 4), padDisplay(direction, 4), padDisplay(status, 8),
			padDisplay(fileProgress, 17), padDisplay(start, 13), padDisplay(end, 13), truncatePath(path, 42),
		}, " "))
	}
	fmt.Printf("\n数据库: %s（aos stat -id <ID> 查看详情）\n", dbPath)
	return 0
}

// padDisplay 按终端显示宽度左对齐补空格：全角字符（中文等）占 2 列，半角占 1 列。
func padDisplay(s string, width int) string {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// runeWidth 返回字符的终端显示宽度（常用全角区间按 2 列计）。
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
		(r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE10 && r <= 0xFE19) ||
		(r >= 0xFE30 && r <= 0xFE6F) || (r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6)):
		return 2
	default:
		return 1
	}
}

// truncatePath 截断过长路径，保留首尾（按字符，避免中文乱码）。
func truncatePath(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func statDetail(database *db.DB, dbPath string, id int64) int {
	t, err := database.GetTask(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: %v\n", err)
		return 1
	}
	fmt.Printf("任务 %d  方向: %s  状态: %s\n", t.ID, taskDirection(t), t.Status)
	fmt.Printf("  本地路径:   %s\n", t.LocalPath)
	fmt.Printf("  云上路径:   %s\n", t.RemotePrefix)
	progress := fmt.Sprintf(" 文件进度:   %d/%d  （%s/%s）", t.DoneFiles, t.TotalFiles, human.Size(t.DoneBytes), human.Size(t.TotalBytes))
	if t.FailedFiles > 0 {
		progress += fmt.Sprintf("，失败 %d", t.FailedFiles)
	}
	fmt.Println(progress)
	fmt.Printf("  开始:       %s\n", t.StartedAt.Format("2006-01-02 15:04"))
	if !t.FinishedAt.IsZero() {
		fmt.Printf("  结束:       %s（用时 %s）\n", t.FinishedAt.Format("2006-01-02 15:04"), t.FinishedAt.Sub(t.StartedAt).Round(time.Second))
	}
	if t.Error != "" {
		fmt.Printf("  错误:       %s\n", t.Error)
	}
	fmt.Printf("  数据库: %s\n", dbPath)
	return 0
}

func statusLine(t db.Task) string {
	if t.Status == "break" {
		return "(break)"
	}
	return "…"
}

func taskDirection(t db.Task) string {
	if t.Direction == "" {
		return "up"
	}
	return t.Direction
}
