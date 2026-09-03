package tosx

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ACME2026001/PM-ACME2026001-01/dataset/a.txt", "ACME2026001/PM-ACME2026001-01/dataset/a.txt"},
		{"./abc//de/./f", "abc/de/f"},
		{"abc//de", "abc/de"},
		{"a/./b", "a/b"},
		{".//abc/de", "abc/de"},
		{"a/../b", "a/b"},
		{"../etc/passwd", "etc/passwd"},
		{"//", ""},
	}
	for _, c := range cases {
		if got := normalizeKey(c.in); got != c.want {
			t.Errorf("normalizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/real.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := makeSymlink(dir+"/real.txt", dir+"/link.txt"); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	if !isSymlink(dir + "/link.txt") {
		t.Error("link.txt 应识别为软链接")
	}
	if isSymlink(dir + "/real.txt") {
		t.Error("real.txt 不应识别为软链接")
	}
}

func TestParsePartSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"20MB", 20 * 1024 * 1024, false},
		{"20m", 20 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"10485760", 10 * 1024 * 1024, false},
		{"512KB", 0, true},                 // 低于 5MB 下限
		{"6GB", 0, true},                   // 高于 5GB 上限
		{"9223372036854775807GB", 0, true}, // n*mult 溢出 int64 应被拦截
		{"abc", 0, true},                   // 非数字
		{"", 0, true},                      // 空
	}
	for _, c := range cases {
		got, err := ParsePartSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParsePartSize(%q): want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePartSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePartSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPartSizeAndTaskNumDefaults(t *testing.T) {
	if got := partSizeOrDefault(0); got != defaultPartSize {
		t.Fatalf("partSizeOrDefault(0) = %d", got)
	}
	if got := partSizeOrDefault(8 * 1024 * 1024); got != 8*1024*1024 {
		t.Fatalf("partSizeOrDefault(8MB) = %d", got)
	}
	if got := taskNumOrDefault(0); got != multipartTaskNum {
		t.Fatalf("taskNumOrDefault(0) = %d", got)
	}
	if got := taskNumOrDefault(8); got != 8 {
		t.Fatalf("taskNumOrDefault(8) = %d", got)
	}
}

// ---- collectUploadJobs：软链接处理 ----

func TestCollectCountsTmpSkipped(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.txt", "b.tmp", "c.checkpoint", ".DS_Store"} {
		if err := writeTestFile(filepath.Join(dir, f), "x"); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(dir, "C/SPI/x", UploadOptions{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if collected.skippedTmp != 2 { // b.tmp + c.checkpoint（.DS_Store 不计入 tmp 统计）
		t.Fatalf("skippedTmp = %d, want 2", collected.skippedTmp)
	}
	if len(collected.jobs) != 1 || collected.jobs[0].local != filepath.Join(dir, "a.txt") {
		t.Fatalf("jobs = %+v", collected.jobs)
	}
	buf.Reset()
	printLinkSkipSummary(&buf, collected)
	if !strings.Contains(buf.String(), "*.tmp/*.checkpoint") {
		t.Fatalf("提示信息应提及 tmp 跳过: %q", buf.String())
	}
}

func TestCollectDefaultConvertsSymlinksToText(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/real.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := makeSymlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	// 断链：readlink 能成功但目标不存在，默认模式仍转文本上传
	if err := makeSymlink(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(dir, "C/SPI/x", UploadOptions{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if collected.linkCount != 2 {
		t.Fatalf("linkCount = %d, want 2", collected.linkCount)
	}
	if len(collected.jobs) != 3 { // real.txt + link.txt（转文本） + broken（转文本）
		t.Fatalf("jobs = %+v", collected.jobs)
	}
	var linkJob, brokenJob *uploadJob
	for i := range collected.jobs {
		switch collected.jobs[i].key {
		case "C/SPI/x/link.txt":
			linkJob = &collected.jobs[i]
		case "C/SPI/x/broken":
			brokenJob = &collected.jobs[i]
		}
	}
	if linkJob == nil || linkJob.linkTarget != filepath.Join(dir, "real.txt") {
		t.Fatalf("link.txt 应转为文本上传，内容为链接目标: %+v", linkJob)
	}
	if linkJob.followLink {
		t.Fatal("默认模式不应有溯源 job")
	}
	if brokenJob == nil || brokenJob.linkTarget != filepath.Join(dir, "missing.txt") {
		t.Fatalf("断链也应转文本上传: %+v", brokenJob)
	}
}

func TestCollectTopLevelSymlinkDefault(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(filepath.Join(dir, "real.txt"), "x"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "top")
	if err := makeSymlink(filepath.Join(dir, "real.txt"), link); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(link, "C/SPI/x", UploadOptions{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if collected.linkCount != 1 || len(collected.jobs) != 1 {
		t.Fatalf("jobs = %+v", collected.jobs)
	}
	j := collected.jobs[0]
	if j.linkTarget != filepath.Join(dir, "real.txt") || j.key != "C/SPI/x" {
		t.Fatalf("顶层链接应转文本上传: %+v", j)
	}
}

func TestCollectFollowLinksFileSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/target/real.bam", "data"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bam")
	if err := makeSymlink(filepath.Join(dir, "target", "real.bam"), link); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(dir, "C/SPI/x", UploadOptions{FollowLinks: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.jobs) != 2 { // target/real.bam + 溯源的 link.bam
		t.Fatalf("jobs = %d, want 2: %+v", len(collected.jobs), collected.jobs)
	}
	var linkJob *uploadJob
	for i := range collected.jobs {
		if collected.jobs[i].key == "C/SPI/x/link.bam" {
			linkJob = &collected.jobs[i]
		}
	}
	if linkJob == nil {
		t.Fatalf("未找到溯源链接 job: %+v", collected.jobs)
	}
	if !linkJob.followLink {
		t.Fatal("溯源链接 job 应标记 followLink")
	}
	if linkJob.local != filepath.Join(dir, "target", "real.bam") {
		t.Fatalf("local = %q, want 链接目标的真实路径", linkJob.local)
	}
	if collected.linkCount != 0 || collected.brokenLinks != 0 {
		t.Fatalf("linkCount=%d broken=%d", collected.linkCount, collected.brokenLinks)
	}
}

func TestCollectFollowLinksDirSymlink(t *testing.T) {
	dir := t.TempDir()
	// 项目目录里有指向外部共享目录的链接
	shared := filepath.Join(dir, "shared")
	if err := writeTestFile(filepath.Join(shared, "sub", "a.txt"), "a"); err != nil {
		t.Fatal(err)
	}
	if err := makeSymlink(shared, filepath.Join(dir, "data")); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(dir, "C/SPI/x", UploadOptions{FollowLinks: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, j := range collected.jobs {
		if j.key == "C/SPI/x/data/sub/a.txt" {
			found = true
			if !j.followLink {
				t.Fatal("目录链接展开的文件应标记 followLink（不记录任务）")
			}
			if j.local != filepath.Join(shared, "sub", "a.txt") {
				t.Fatalf("local = %q", j.local)
			}
		}
	}
	if !found {
		t.Fatalf("未找到目录链接展开的 job: %+v", collected.jobs)
	}
}

func TestCollectFollowLinksBrokenAndCycle(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(filepath.Join(dir, "real.txt"), "x"); err != nil {
		t.Fatal(err)
	}
	// 断链
	if err := makeSymlink(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	// 循环：dir/link -> dir
	if err := makeSymlink(dir, filepath.Join(dir, "cycle")); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(dir, "C/SPI/x", UploadOptions{FollowLinks: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if collected.brokenLinks < 1 {
		t.Fatalf("brokenLinks = %d, want >= 1（输出: %s）", collected.brokenLinks, buf.String())
	}
	if !strings.Contains(buf.String(), "跳过循环软链接") {
		t.Fatalf("应提示循环链接:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "跳过断链软链接") {
		t.Fatalf("应提示断链:\n%s", buf.String())
	}
	// 不死循环：jobs 数量有限（real.txt 出现 2 次：主遍历 + cycle 首次展开）
	if len(collected.jobs) > 5 {
		t.Fatalf("疑似循环展开过多 job: %d", len(collected.jobs))
	}
}

func TestCollectTopLevelSymlinkFollow(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(filepath.Join(dir, "real.bam"), "data"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "top")
	if err := makeSymlink(filepath.Join(dir, "real.bam"), link); err != nil {
		t.Skipf("无法创建软链接: %v", err)
	}
	var buf bytes.Buffer
	collected, err := collectUploadJobs(link, "C/SPI/x", UploadOptions{FollowLinks: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.jobs) != 1 {
		t.Fatalf("jobs = %+v", collected.jobs)
	}
	if !collected.jobs[0].followLink || collected.jobs[0].key != "C/SPI/x" {
		t.Fatalf("顶层链接溯源 job 异常: %+v", collected.jobs[0])
	}
}
