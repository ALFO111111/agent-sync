package main

import "testing"

func TestParseHistory(t *testing.T) {
	data := []byte(`{"type":"user","timestamp":"2026-11-12T10:00:00Z","message":{"content":"你好"}}
{"type":"assistant","timestamp":"2026-11-12T10:00:01Z","message":{"content":[{"type":"text","text":"你好！"},{"type":"tool_use","name":"Read"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"工具结果"}]}}
{"type":"summary","summary":"x"}
`)
	msgs := parseHistory(data)
	if len(msgs) != 3 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "你好" || msgs[0].Time == "" {
		t.Fatalf("user = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Text != "你好！" {
		t.Fatalf("assistant = %+v", msgs[1])
	}
	if msgs[2].Text != "工具结果" {
		t.Fatalf("last = %+v", msgs[2])
	}
}
