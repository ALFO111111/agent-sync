package main

import "testing"

func TestProductVersion(t *testing.T) {
	for input, want := range map[string]string{"dev": "0.0.0", "v0.6.1": "0.6.1", "1.2.3-rc.1": "1.2.3", "1.2.3+build": "1.2.3"} {
		got, err := productVersion(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "v1.2", "one", "1.2.3 -X evil", "1.2.3\n"} {
		if _, err := productVersion(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
