package main

import (
	"testing"
	"time"
)

// Windows 上路径不区分大小写；会话索引里的盘符可能是小写，手机也可能发大写。
func TestCwdAllowedCaseInsensitive(t *testing.T) {
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
