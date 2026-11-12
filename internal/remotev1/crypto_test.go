package remotev1

import (
	"bytes"
	"testing"
)

func mustKey(t *testing.T) ContentKey {
	t.Helper()
	k, err := NewContentKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := mustKey(t)
	s, err := NewSealer(k, ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewOpener(k, ClientToHost)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		plain := []byte("hello agent-sync")
		frame, err := s.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := o.Open(frame)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("roundtrip %q != %q", got, plain)
		}
	}
}

func TestOpenRejectsWrongKeyAADAndTamper(t *testing.T) {
	k := mustKey(t)
	s, _ := NewSealer(k, ClientToHost)
	frame, _ := s.Seal([]byte("secret"))

	other := mustKey(t)
	o, _ := NewOpener(other, ClientToHost)
	if _, err := o.Open(frame); err == nil {
		t.Fatal("wrong key accepted")
	}

	o2, _ := NewOpener(k, HostToClient)
	if _, err := o2.Open(frame); err == nil {
		t.Fatal("wrong direction AAD accepted")
	}

	o3, _ := NewOpener(k, ClientToHost)
	bad := append([]byte(nil), frame...)
	bad[len(bad)-1] ^= 0xff
	if _, err := o3.Open(bad); err == nil {
		t.Fatal("tampered frame accepted")
	}
}

func TestOpenRejectsReplayAndGaps(t *testing.T) {
	k := mustKey(t)
	s, _ := NewSealer(k, ClientToHost)
	o, _ := NewOpener(k, ClientToHost)
	f0, _ := s.Seal([]byte("0"))
	f1, _ := s.Seal([]byte("1"))
	if _, err := o.Open(f0); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Open(f0); err == nil {
		t.Fatal("replay accepted")
	}
	if _, err := o.Open(f1); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Open(f1); err == nil {
		t.Fatal("duplicate accepted")
	}

	// 跳过一帧（序号缺口）必须拒绝。
	s2, _ := NewSealer(k, ClientToHost)
	o2, _ := NewOpener(k, ClientToHost)
	_, _ = s2.Seal([]byte("skip"))
	f2, _ := s2.Seal([]byte("next"))
	if _, err := o2.Open(f2); err == nil {
		t.Fatal("gap accepted")
	}

	// 另一条连接的前缀不能被混用。
	s3, _ := NewSealer(k, ClientToHost)
	f3, _ := s3.Seal([]byte("other"))
	if _, err := o2.Open(f3); err == nil {
		t.Fatal("prefix change accepted")
	}
}

func TestSealRejectsOversizeAndParseKey(t *testing.T) {
	k := mustKey(t)
	if got, err := ParseContentKey(k.Base64()); err != nil || got != k {
		t.Fatal("key roundtrip", err)
	}
	if _, err := ParseContentKey("bad"); err == nil {
		t.Fatal("bad key accepted")
	}
	s, _ := NewSealer(k, ClientToHost)
	if _, err := s.Seal(make([]byte, MaxPlain+1)); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := s.Seal(nil); err == nil {
		t.Fatal("empty accepted")
	}
}
