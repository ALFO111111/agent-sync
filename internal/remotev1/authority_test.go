package remotev1

import (
	"fmt"
	"testing"
	"time"
)

func TestPendingExpiresAfterFiveMinutes(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	inv, err := auth.Invite("host")
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.BeginPair(inv.ID, inv.Secret, "pub", "phone")
	if err != nil {
		t.Fatal(err)
	}
	auth.mu.Lock()
	auth.pending[p.ID].Expires = time.Now().Add(-time.Second)
	auth.mu.Unlock()

	if got := auth.PendingList(); len(got) != 0 {
		t.Fatalf("pending = %d, want 0", len(got))
	}
	if err := auth.Decide(p.ID, true); err == nil {
		t.Fatal("decide on expired pending should fail")
	}
}

func TestPendingExpiryDoesNotBlockNewPairs(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	inv, _ := auth.Invite("host")
	for i := 0; i < maxPending; i++ {
		p, err := auth.BeginPair(inv.ID, inv.Secret, fmt.Sprintf("pub-%d", i), "phone")
		if err != nil {
			t.Fatalf("pair %d: %v", i, err)
		}
		auth.mu.Lock()
		auth.pending[p.ID].Expires = time.Now().Add(-time.Second)
		auth.mu.Unlock()
	}
	if _, err := auth.BeginPair(inv.ID, inv.Secret, "pub-new", "phone"); err != nil {
		t.Fatalf("expired pendings blocked a new pair: %v", err)
	}
}

func TestBeginPairStoresDeviceKey(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	inv, err := auth.Invite("host")
	if err != nil {
		t.Fatal(err)
	}
	devKey, _ := NewContentKey()
	p, err := auth.BeginPair(inv.ID, inv.Secret, "pub-1", "phone", devKey.Base64())
	if err != nil {
		t.Fatal(err)
	}
	if p.ContentKey != devKey.Base64() {
		t.Fatalf("pending key = %q", p.ContentKey)
	}
	if err := auth.Decide(p.ID, true); err != nil {
		t.Fatal(err)
	}
	d, ok := auth.Device("pub-1")
	if !ok || d.ContentKey != devKey.Base64() {
		t.Fatalf("device key = %q, ok=%v", d.ContentKey, ok)
	}
	if !auth.IsActive(d.ID) {
		t.Fatal("new device should be active")
	}
}

func TestProofV2BindsDeviceKey(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublic(id.PublicBase64())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewContentKey()
	sig := id.SignProofV2("nonce", "inv", key.Base64())
	if !VerifyProof(pub, "nonce", id.PublicBase64(), "inv", sig, key.Base64()) {
		t.Fatal("v2 proof should verify with the same device key")
	}
	other, _ := NewContentKey()
	if VerifyProof(pub, "nonce", id.PublicBase64(), "inv", sig, other.Base64()) {
		t.Fatal("v2 proof must not verify with a different device key")
	}
}

func TestRevokeClearsDeviceKey(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	devKey, _ := NewContentKey()
	auth.state.Devices = append(auth.state.Devices, Device{
		ID: "d1", Name: "phone", PublicKey: "pub-1", Fingerprint: "fp-1", ContentKey: devKey.Base64(),
	})
	dev, err := auth.Revoke("d1")
	if err != nil {
		t.Fatal(err)
	}
	if dev.ContentKey != "" {
		t.Fatalf("revoked device kept key %q", dev.ContentKey)
	}
	d, ok := auth.Device("pub-1")
	if !ok || !d.Revoked || d.ContentKey != "" {
		t.Fatalf("stored device = %+v", d)
	}
	if auth.IsActive("d1") {
		t.Fatal("revoked device still active")
	}
}
