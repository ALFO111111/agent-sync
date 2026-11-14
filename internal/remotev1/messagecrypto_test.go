package remotev1

import "testing"

func TestSealOpenFrame(t *testing.T) {
	key, _ := NewContentKey()
	f, err := SealFrame(key, ClientToHost, "dev1", 7, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if f.From != "dev1" || f.Seq != 7 || f.V != 3 {
		t.Fatalf("unexpected frame: %+v", f)
	}
	plain, err := OpenFrame(key, ClientToHost, "dev1", 7, f)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("OpenFrame = %q, %v", plain, err)
	}
	if _, err := OpenFrame(key, ClientToHost, "dev2", 7, f); err == nil {
		t.Fatal("wrong sender must fail")
	}
	if _, err := OpenFrame(key, HostToClient, "dev1", 7, f); err == nil {
		t.Fatal("wrong direction must fail")
	}
}

func TestReplayWindow(t *testing.T) {
	w := NewReplayWindow(8)
	for _, seq := range []uint64{1, 2, 3} {
		if !w.Accept(seq) {
			t.Fatalf("seq %d should be accepted", seq)
		}
	}
	if w.Accept(3) {
		t.Fatal("duplicate must be rejected")
	}
	if !w.Accept(5) {
		t.Fatal("in-window gap should be accepted")
	}
	if w.Accept(1) {
		t.Fatal("old duplicate must be rejected")
	}
	if !w.Accept(100) {
		t.Fatal("jump forward should be accepted")
	}
	if w.Accept(50) {
		t.Fatal("too old must be rejected")
	}
}

func TestMaxPlainAllows64K(t *testing.T) {
	if MaxPlain < 64*1024 {
		t.Fatalf("MaxPlain = %d, want >= 64 KiB", MaxPlain)
	}
}
