package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cp 方向判定：错误路径在访问网络前就能被拦截，可直接用 run() 验证退出码。
func TestCPDirectionErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"cp", "./a", "./b"}, "本地到本地拷贝不支持"},
		{[]string{"cp", "tos://a/x", "tos://b/y"}, "云端拷贝"},
		{[]string{"cp"}, "缺少参数"},
		{[]string{"cp", "a", "b", "c"}, "最多 2 个位置参数"},
	}
	for _, c := range cases {
		code := run(c.args)
		if code != 2 {
			t.Errorf("run(%v) = %d, want 2", c.args, code)
		}
	}
}

// 单参数本地路径 = 按上传记录回查还原：无配置/无记录时给出明确提示。
func TestCPSingleLocalPathLookup(t *testing.T) {
	dbDir := t.TempDir()
	os.Setenv("AOS_AK", "ak")
	os.Setenv("AOS_SK", "sk")
	os.Setenv("AOS_REGION", "cn-beijing")
	os.Setenv("AOS_DB", filepath.Join(dbDir, "aos.db"))
	t.Cleanup(func() {
		os.Unsetenv("AOS_AK")
		os.Unsetenv("AOS_SK")
		os.Unsetenv("AOS_REGION")
		os.Unsetenv("AOS_DB")
	})
	// 无任务记录：报错并提示上传方式
	if code := run([]string{"cp", "./no-such-upload-path"}); code != 1 {
		t.Fatalf("单参数本地路径无记录应报错（exit 1），实际 %d", code)
	}
	// 零位置参数直接报缺少参数
	if code := run([]string{"cp"}); code != 2 {
		t.Fatalf("零参数应报缺少参数（exit 2），实际 %d", code)
	}
}

func TestCPRejectsRemovedSubcommands(t *testing.T) {
	for _, cmd := range []string{"up", "down", "upload", "dl"} {
		if code := run([]string{cmd}); code != 2 {
			t.Errorf("run(%q) = %d, want 2（应提示未知命令）", cmd, code)
		}
	}
}

func TestIsTOSPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"tos://bucket/x", true},
		{"tos:///x", true},
		{"./dataset", false},
		{"dataset", false},
		{"/abs/path", false},
	}
	for _, c := range cases {
		if got := strings.Contains(c.in, "://"); got != c.want {
			t.Errorf("isTOS(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// 回归：reorderArgs 不应把布尔 flag 当作前一个非布尔 flag 的值吞掉。
// 修复前：aos cp -j 4 -d -q 会把 local 解析成 "-q"、quiet 保持 false。
func TestReorderArgsDoesNotConsumeBoolFlagAsValue(t *testing.T) {
	fs := flag.NewFlagSet("aos cp", flag.ContinueOnError)
	local := fs.String("d", "", "")
	quiet := fs.Bool("q", false, "")
	job := fs.Int("j", 0, "")
	args := reorderArgs(fs, []string{"-j", "4", "-d", "-q"})
	ok, err := parseFlagSet(fs, args, "usage")
	if ok || err == nil {
		t.Fatalf("-d 缺参数应报错，实际 ok=%v local=%q quiet=%v job=%d", ok, *local, *quiet, *job)
	}
	if !strings.Contains(err.Error(), "flag needs an argument: -d") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
	if *local != "" {
		t.Fatalf("local 不应被误赋值: %q", *local)
	}
}

func TestValidateNoFlagAsValueAllowsDashValue(t *testing.T) {
	fs := flag.NewFlagSet("aos cp", flag.ContinueOnError)
	maxDepth := fs.Int("max-depth", 0, "")
	// 值以 - 开头但不是已注册 flag：留给 flag 包处理（如 -max-depth -1）
	if err := validateNoFlagAsValue(fs, []string{"-max-depth", "-1"}); err != nil {
		t.Fatalf("不应拦截非 flag 的 - 前缀值: %v", err)
	}
	_ = fs.Parse([]string{"-max-depth", "-1"})
	if *maxDepth != -1 {
		t.Fatalf("maxDepth = %d", *maxDepth)
	}
}

func TestValidateNoFlagAsValueCatchesUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("aos up", flag.ContinueOnError)
	local := fs.String("d", "", "")
	// 未知 flag 不应被吞成前一个 flag 的值：-d -x 应报错而非 d="-x"
	ok, err := parseFlagSet(fs, []string{"-d", "-x"}, "usage")
	if ok || err == nil {
		t.Fatalf("未知 flag 应被拦截，实际 ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "-d") {
		t.Fatalf("错误信息未指向 -d: %v", err)
	}
	if *local != "" {
		t.Fatalf("local 不应被误赋值: %q", *local)
	}
}

func TestValidateNoFlagAsValueAllowsNegativeNumberValue(t *testing.T) {
	fs := flag.NewFlagSet("aos up", flag.ContinueOnError)
	thread := fs.Int("thread", 0, "")
	// 负数不是 flag，应放行让 flag 包解析
	if err := validateNoFlagAsValue(fs, []string{"-thread", "-4"}); err != nil {
		t.Fatalf("负数不应被拦截: %v", err)
	}
	_ = fs.Parse([]string{"-thread", "-4"})
	if *thread != -4 {
		t.Fatalf("thread = %d", *thread)
	}
}

func TestReorderArgsKeepsFlagValues(t *testing.T) {
	fs := flag.NewFlagSet("aos cp", flag.ContinueOnError)
	spiID := fs.String("spi", "", "")
	local := fs.String("d", "", "")
	quiet := fs.Bool("q", false, "")
	exclude := fs.String("e", "", "")
	_ = fs.Parse(reorderArgs(fs, []string{"-spi", "PM-x-01", "-d", "/local", "-q", "-e", "*.tmp,.git"}))
	if *spiID != "PM-x-01" {
		t.Fatalf("spi = %q", *spiID)
	}
	if *local != "/local" {
		t.Fatalf("local = %q, want /local", *local)
	}
	if !*quiet {
		t.Fatal("quiet 应被设置")
	}
	if *exclude != "*.tmp,.git" {
		t.Fatalf("exclude = %q", *exclude)
	}
}

func TestReorderArgsMovesPositionalsToEnd(t *testing.T) {
	fs := flag.NewFlagSet("aos cp", flag.ContinueOnError)
	local := fs.String("d", "", "")
	args := reorderArgs(fs, []string{"tos://b/C/SPI/dataset", "-d", "/local"})
	if len(args) != 3 || args[0] != "-d" || args[1] != "/local" || args[2] != "tos://b/C/SPI/dataset" {
		t.Fatalf("reordered = %v", args)
	}
	_ = fs.Parse(args)
	if *local != "/local" {
		t.Fatalf("local = %q", *local)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "tos://b/C/SPI/dataset" {
		t.Fatalf("positional = %v", fs.Args())
	}
}
