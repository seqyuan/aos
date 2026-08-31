package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/seqyuan/aos/internal/db"
	"github.com/seqyuan/aos/internal/human"
)

// cmdStat aos stat：查询 cp 任务状态（done/break，含进度）。
func cmdStat(args []string) int {
	fs := flag.NewFlagSet("aos stat", flag.ContinueOnError)
	spiFilter := fs.String("spi", "", "按 SPI 编号过滤")
	taskID := fs.Int64("id", 0, "查看指定任务详情（含软链接记录）")
	limit := fs.Int("limit", 20, "最多列出多少条")
	dbPath := fs.String("db", "", "sqlite 数据库路径（默认 ~/.config/aos.db）")
	if ok, err := parseFlagSet(fs, args, "用法: aos stat [选项]\n\n示例:\n  aos stat                                      # 最近任务\n  aos stat -spi PM-ACME2026001-01         # 某子项目任务\n  aos stat -id 3                                # 任务详情 + 软链接记录"); !ok {
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
	return statList(database, path, *spiFilter, *limit)
}

func statList(database *db.DB, dbPath, spiFilter string, limit int) int {
	tasks, err := database.ListTasks(spiFilter, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		if spiFilter != "" {
			fmt.Printf("没有 %s 的任务记录（数据库 %s）\n", spiFilter, dbPath)
		} else {
			fmt.Printf("还没有任务记录（数据库 %s）\n", dbPath)
		}
		return 0
	}
	fmt.Printf("%-4s %-26s %-8s %-18s %-13s %s\n", "ID", "SPI", "状态", "文件进度", "开始时间", "完成/错误")
	fmt.Println(strings.Repeat("-", 104))
	for _, t := range tasks {
		status := t.Status
		if t.Status == "running" && time.Since(t.UpdatedAt) > 30*time.Second {
			status = "running?"
		}
		fileProgress := fmt.Sprintf("%d/%d", t.DoneFiles, t.TotalFiles)
		if t.FailedFiles > 0 {
			fileProgress += fmt.Sprintf("(失败%d)", t.FailedFiles)
		}
		start := t.StartedAt.Format("01-02 15:04:05")
		end := ""
		if !t.FinishedAt.IsZero() {
			end = t.FinishedAt.Format("01-02 15:04:05")
		} else {
			end = statusLine(t)
		}
		fmt.Printf("%-4d %-26s %-8s %-18s %-13s %s\n",
			t.ID, t.SPI, status, fileProgress, start, end)
	}
	fmt.Printf("\n数据库: %s（用 aos stat -id <ID> 查看详情与软链接记录）\n", dbPath)
	return 0
}

func statDetail(database *db.DB, dbPath string, id int64) int {
	t, err := database.GetTask(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: %v\n", err)
		return 1
	}
	fmt.Printf("任务 %d  状态: %s\n", t.ID, t.Status)
	fmt.Printf("  SPI:        %s\n", t.SPI)
	fmt.Printf("  contract:   %s\n", t.Contract)
	fmt.Printf("  -d:         %s\n", t.LocalPath)
	if t.RemotePrefix != "" {
		fmt.Printf("  远端:       %s\n", t.RemotePrefix)
	}
	progress := fmt.Sprintf(" 文件进度:   %d/%d  （%s/%s）", t.DoneFiles, t.TotalFiles, human.Size(t.DoneBytes), human.Size(t.TotalBytes))
	if t.FailedFiles > 0 {
		progress += fmt.Sprintf("，失败 %d", t.FailedFiles)
	}
	if t.LinkCount > 0 {
		progress += fmt.Sprintf("，软链接 %d", t.LinkCount)
	}
	fmt.Println(progress)
	fmt.Printf("  开始:       %s\n", t.StartedAt.Format("2006-01-02 15:04:05"))
	if !t.FinishedAt.IsZero() {
		fmt.Printf("  结束:       %s（用时 %s）\n", t.FinishedAt.Format("2006-01-02 15:04:05"), t.FinishedAt.Sub(t.StartedAt).Round(time.Second))
	}
	if t.Error != "" {
		fmt.Printf("  错误:       %s\n", t.Error)
	}

	links, err := database.GetLinks(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos stat: %v\n", err)
		return 1
	}
	if len(links) > 0 {
		fmt.Printf("  软链接记录 (%d)：\n", len(links))
		for _, l := range links {
			fmt.Printf("    %s -> %s   (对象: %s)\n", l.LinkRel, l.LinkTarget, l.ObjectKey)
		}
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
