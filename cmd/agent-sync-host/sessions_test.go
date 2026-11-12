package main

import (
	"runtime"
	"testing"
	"time"
)

// Windows 上路径不区分大小写；会话索引里的盘符可能是小写，手机也可能发大写。
// 用例里的路径是 Windows 写法，靠 filepath.Separator 判断是否在目录内，macOS/Linux 上跑不出真实语义。
func TestCwdAllowedCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	s := &sessionService{
		allowed:     []string{`d:\developProject\agent-sync`, `C:\Users\me`},
		allowedTime: time.Now(),
	}
	cases := []struct {
		cwd  string
		want bool
	}{
		{`D:\developProject\agent-sync`, true},           // 盘符大小写不同
		{`d:\developproject\AGENT-SYNC`, true},           // 整条路径大小写不同
		{`D:\developProject\agent-sync\cmd`, true},       // 子目录
		{`D:\developProject\agent-sync2`, false},         // 只是前缀相同，不是同一个目录
		{`C:\Users\me\file`, true},                       // 用户目录下
		{`C:\Users\melon`, false},                        // 前缀相同但目录不同
		{`relative\path`, false},                         // 相对路径
		{`D:\developProject\agent-sync\..\other`, false}, // .. 逃逸
	}
	for _, tc := range cases {
		if got := s.cwdAllowed(tc.cwd); got != tc.want {
			t.Errorf("cwdAllowed(%q) = %v, want %v", tc.cwd, got, tc.want)
		}
	}
}
