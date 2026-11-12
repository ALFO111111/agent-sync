package main

import (
	"os"
	"runtime"
	"time"

	"github.com/ALFO111111/agent-sync/internal/config"
	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
	"github.com/ALFO111111/agent-sync/internal/state"
)

// Overview 汇总手机首页/设置要展示的电脑概况；只读，不碰密钥和云端凭据。
func (s *sessionService) Overview() (remotev1.HostOverview, error) {
	cfg, err := config.Load()
	if err != nil {
		return remotev1.HostOverview{}, err
	}
	st, _ := state.Load()
	if st == nil {
		st = &state.State{}
	}
	ov := remotev1.HostOverview{
		DeviceName: cfg.DeviceName,
		System:     runtime.GOOS,
		Language:   i18n.Lang(),
		DataDir:    config.Dir(),
		Stats: remotev1.OverviewStats{
			Pushed: st.LastStats.Pushed,
			Pulled: st.LastStats.Pulled,
			Merged: st.LastStats.Merged,
			Forked: st.LastStats.Forked,
			Errors: st.LastStats.Errors,
		},
	}
	if !st.LastSync.IsZero() {
		ov.LastSync = st.LastSync.Format(time.RFC3339)
	}

	tools := []struct {
		id, name, dir string
		cats          []string
	}{
		{config.ToolClaude, "Claude Code", cfg.ClaudeDir, config.ContentCategories},
		{config.ToolCodex, "Codex", cfg.CodexDir, config.CodexContentCategories},
	}
	for _, t := range tools {
		sum := remotev1.ToolSummary{ID: t.id, Name: t.name}
		if fi, err := os.Stat(t.dir); err == nil && fi.IsDir() {
			sum.Installed = true
		}
		switch t.id {
		case config.ToolClaude:
			if s.index != nil {
				if list, err := s.index.Projects(); err == nil {
					sum.Projects = len(list)
					for _, p := range list {
						sum.SessionCount += p.Sessions
					}
				}
			}
		case config.ToolCodex:
			if s.codexIdx != nil {
				if list, err := s.codexIdx.Projects(); err == nil {
					sum.Projects = len(list)
					for _, p := range list {
						sum.SessionCount += p.Sessions
					}
				}
			}
		}
		for _, cat := range t.cats {
			enabled := cfg.ContentEnabledFor(t.id, cat)
			if enabled {
				sum.ContentOn++
			}
			ov.Content = append(ov.Content, remotev1.ContentToggle{Tool: t.id, Category: cat, Enabled: enabled})
		}
		sum.ContentTotal = len(t.cats)
		ov.Projects += sum.Projects
		ov.Sessions += sum.SessionCount
		ov.Tools = append(ov.Tools, sum)
	}
	return ov, nil
}
