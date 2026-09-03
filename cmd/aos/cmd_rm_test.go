package main

import "testing"

// rm 命令的错误路径在访问网络前就能被拦截，可直接用 run() 验证退出码。
func TestRMArgErrors(t *testing.T) {
	cases := [][]string{
		{"rm"},                           // 缺参数
		{"rm", "tos://a/x", "tos://b/y"}, // 多参数（>1）
		{"rm", "./local/data"},           // 本地路径被拒绝（必须 tos://）
		{"rm", "-r"},                     // 有 flag 但缺位置参数
	}
	for _, args := range cases {
		if code := run(args); code != 2 {
			t.Errorf("run(%v) 期望 exit 2，实际 %d", args, code)
		}
	}
}
