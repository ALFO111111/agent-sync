package remotev1

import (
	"reflect"
	"testing"
)

func TestTopicRoundTrip(t *testing.T) {
	ts := TopicSet{UID: "aa", HID: "bb", DID: "cc"}
	cases := []struct {
		topic string
		kind  string
		did   string
	}{
		{ts.Req(), "req", "cc"},
		{ts.Evt(), "evt", ""},
		{ts.Up(), "up", "cc"},
		{ts.Status(), "status", ""},
		{ts.Pair(), "pair", "cc"},
	}
	for _, c := range cases {
		kind, uid, hid, did, ok := ParseTopic(c.topic)
		if !ok || kind != c.kind || uid != "aa" || hid != "bb" || did != c.did {
			t.Fatalf("ParseTopic(%q) = %q,%q,%q,%q,%v", c.topic, kind, uid, hid, did, ok)
		}
	}
}

func TestHostSubscriptions(t *testing.T) {
	ts := TopicSet{UID: "aa", HID: "bb", DID: "cc"}
	want := []string{
		"u/aa/h/bb/req/+",
		"u/aa/h/bb/up/+",
		"u/aa/h/bb/pair/+",
	}
	if got := ts.HostSubscriptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("HostSubscriptions() = %v, want %v", got, want)
	}
}

func TestParseTopicRejectsBadInput(t *testing.T) {
	for _, s := range []string{"", "u/aa", "x/aa/h/bb/evt", "u/aa/h/bb/unknown", "u/aa/h/bb/req/cc/extra"} {
		if _, _, _, _, ok := ParseTopic(s); ok {
			t.Fatalf("ParseTopic(%q) should fail", s)
		}
	}
}
