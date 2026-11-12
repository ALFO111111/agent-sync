package remotev1

import (
	"context"
	"testing"
	"time"
)

type fakeOverview struct{}

func (fakeOverview) Overview() (HostOverview, error) {
	return HostOverview{
		DeviceName: "Workstation",
		System:     "windows",
		Language:   "zh-CN",
		Projects:   2,
		Sessions:   7,
		LastSync:   "2026-11-12T19:00:00Z",
		Stats:      OverviewStats{Pushed: 3, Pulled: 1},
		Tools: []ToolSummary{
			{ID: "claude", Name: "Claude Code", Installed: true, Projects: 2, SessionCount: 7, ContentOn: 8, ContentTotal: 8},
		},
		Content: []ContentToggle{{Tool: "claude", Category: "rules", Enabled: true}},
	}, nil
}

func TestOverviewOverEncryptedChannel(t *testing.T) {
	f := setup(t)
	f.host.Data = fakeOverview{}
	device, _ := NewIdentity()
	c := NewClient(device, "Pixel", f.inv)
	done := make(chan error, 1)
	go func() { done <- c.Pair(context.Background()) }()
	approveNext(t, f)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if err := c.s.write(Envelope{Type: "data.overview", ID: "ov1"}); err != nil {
		t.Fatal(err)
	}
	reply, err := c.s.readWith(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Type != "data.overview" || reply.Overview == nil {
		t.Fatalf("reply = %+v", reply)
	}
	ov := reply.Overview
	if ov.HostName != "Test Mac" || ov.DeviceName != "Workstation" || ov.Projects != 2 ||
		ov.Sessions != 7 || ov.Stats.Pushed != 3 || len(ov.Tools) != 1 || len(ov.Content) != 1 {
		t.Fatalf("overview = %+v", ov)
	}
}

func TestOverviewUnsupported(t *testing.T) {
	f := setup(t)
	device, _ := NewIdentity()
	c := NewClient(device, "Pixel", f.inv)
	done := make(chan error, 1)
	go func() { done <- c.Pair(context.Background()) }()
	approveNext(t, f)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.s.write(Envelope{Type: "data.overview", ID: "ov2"}); err != nil {
		t.Fatal(err)
	}
	reply, err := c.s.readWith(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Type != "data.overview" || reply.Error == "" {
		t.Fatalf("reply = %+v", reply)
	}
}
