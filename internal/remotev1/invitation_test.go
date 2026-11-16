package remotev1

import (
	"strings"
	"testing"
)

func TestInvitationSelfhostAllowsWS(t *testing.T) {
	key, err := NewContentKey()
	if err != nil {
		t.Fatal(err)
	}
	base := Invitation{
		Version:    3,
		URL:        "ws://192.168.1.20:8083/mqtt",
		ID:         strings.Repeat("a", 64),
		Secret:     strings.Repeat("b", 64),
		ContentKey: key.Base64(),
		HostID:     "h1",
		Mode:       "selfhost",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("selfhost ws invitation rejected: %v", err)
	}
	base.Mode = "official"
	if err := base.Validate(); err == nil {
		t.Fatal("official ws invitation should be rejected")
	}
}
