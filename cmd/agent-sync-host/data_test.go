package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ALFO111111/agent-sync/internal/agentsessions"
	"github.com/ALFO111111/agent-sync/internal/config"
	"github.com/ALFO111111/agent-sync/internal/state"
	"github.com/ALFO111111/agent-sync/internal/testutil"
)

func TestOverviewReadsLocalData(t *testing.T) {
	cfg := testutil.NewDevice(t, "overview-test")
	root, dir := testutil.AddProject(t, cfg, "proj")
	line := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"content\":\"hi\"},\"cwd\":%q}\n", root)
	testutil.WriteProjectFile(t, cfg, dir, "11111111-1111-1111-1111-111111111111.jsonl", line)

	if err := state.Save(&state.State{
		LastSync:  time.Now().Add(-time.Minute),
		LastStats: state.SyncStats{Pushed: 2, Pulled: 1, Errors: 0},
	}); err != nil {
		t.Fatal(err)
	}

	s := &sessionService{
		index:    &agentsessions.Index{Root: cfg.ProjectsDir()},
		codexIdx: &agentsessions.CodexIndex{Root: filepath.Join(t.TempDir(), "none")},
	}
	ov, err := s.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if ov.DeviceName != "overview-test" {
		t.Fatalf("device = %q", ov.DeviceName)
	}
	if ov.Projects != 1 || ov.Sessions != 1 {
		t.Fatalf("projects/sessions = %d/%d", ov.Projects, ov.Sessions)
	}
	if ov.LastSync == "" || ov.Stats.Pushed != 2 || ov.Stats.Pulled != 1 {
		t.Fatalf("sync = %q %+v", ov.LastSync, ov.Stats)
	}
	if len(ov.Tools) != 2 || ov.Tools[0].ID != config.ToolClaude || !ov.Tools[0].Installed {
		t.Fatalf("tools = %+v", ov.Tools)
	}
	wantContent := len(config.ContentCategories) + len(config.CodexContentCategories)
	if len(ov.Content) != wantContent {
		t.Fatalf("content = %d, want %d", len(ov.Content), wantContent)
	}
}
