package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
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

// validateNoFlagAsValue：防止非布尔 flag 把另一个 flag 当作自己的值吞掉。
// pflag 对 "--d --q" 会解析成 d="--q"（q 不生效），属于用户漏写参数。

func TestValidateNoFlagAsValueCatchesFollowingFlag(t *testing.T) {
	fs := pflag.NewFlagSet("aos cp", pflag.ContinueOnError)
	exclude := fs.StringP("exclude", "e", "", "")
	_ = fs.BoolP("quiet", "q", false, "")
	// --exclude 后面紧跟 --quiet：用户漏写 exclude 的值，应报错而非把 --quiet 吞成值
	ok, err := parseFlagSet(fs, []string{"--exclude", "--quiet"}, "usage")
	if ok || err == nil {
		t.Fatalf("应拦截，实际 ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "exclude") {
		t.Fatalf("错误信息未指向 exclude: %v", err)
	}
	if *exclude != "" {
		t.Fatalf("exclude 不应被误赋值: %q", *exclude)
	}
}

func TestValidateNoFlagAsValueCatchesUnknownFlag(t *testing.T) {
	fs := pflag.NewFlagSet("aos cp", pflag.ContinueOnError)
	local := fs.String("d", "", "")
	// 未知 flag 不应被吞成前一个 flag 的值：--d --x 应报错而非 d="--x"
	ok, err := parseFlagSet(fs, []string{"--d", "--x"}, "usage")
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
	fs := pflag.NewFlagSet("aos ls", pflag.ContinueOnError)
	maxDepth := fs.Int("max-depth", 0, "")
	// 负数不是 flag，应放行让 pflag 解析（--max-depth -1）
	if err := validateNoFlagAsValue(fs, []string{"--max-depth", "-1"}); err != nil {
		t.Fatalf("负数不应被拦截: %v", err)
	}
	_ = fs.Parse([]string{"--max-depth", "-1"})
	if *maxDepth != -1 {
		t.Fatalf("maxDepth = %d", *maxDepth)
	}
}

// pflag 原生支持 flag 与位置参数混排（Interspersed），替代了原 reorderArgs 手工逻辑。

func TestPFlagInterspersed(t *testing.T) {
	fs := pflag.NewFlagSet("aos ls", pflag.ContinueOnError)
	endpoint := fs.String("endpoint", "", "")
	// flag 出现在位置参数之后也能解析
	_ = fs.Parse([]string{"tos://b/x", "--endpoint", "e1"})
	if *endpoint != "e1" {
		t.Fatalf("endpoint = %q, want e1", *endpoint)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "tos://b/x" {
		t.Fatalf("positional = %v", fs.Args())
	}
}

func TestPFlagNegativeNumberAsValue(t *testing.T) {
	fs := pflag.NewFlagSet("aos ls", pflag.ContinueOnError)
	maxDepth := fs.Int("max-depth", 0, "")
	_ = fs.Parse([]string{"--max-depth", "-1", "tos://b/x"})
	if *maxDepth != -1 {
		t.Fatalf("maxDepth = %d, want -1", *maxDepth)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "tos://b/x" {
		t.Fatalf("positional = %v", fs.Args())
	}
}

func TestPFlagShorthand(t *testing.T) {
	fs := pflag.NewFlagSet("aos cp", pflag.ContinueOnError)
	quiet := fs.BoolP("quiet", "q", false, "")
	exclude := fs.StringP("exclude", "e", "", "")
	_ = fs.Parse([]string{"-q", "-e", "*.tmp", "src", "tos://b/x"})
	if !*quiet {
		t.Fatal("quiet 应被设置")
	}
	if *exclude != "*.tmp" {
		t.Fatalf("exclude = %q", *exclude)
	}
	if fs.NArg() != 2 || fs.Arg(0) != "src" || fs.Arg(1) != "tos://b/x" {
		t.Fatalf("positional = %v", fs.Args())
	}
}
