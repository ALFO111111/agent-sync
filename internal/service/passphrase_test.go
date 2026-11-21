package service

import "testing"

func TestPassphraseTooShort(t *testing.T) {
	for _, p := range []string{"", "short", "12345678901", "中文口令只有六个字"} {
		if err := validatePassphrase(p); err == nil {
			t.Fatalf("passphrase %q should be rejected", p)
		}
	}
}

func TestPassphraseCommonRejected(t *testing.T) {
	for _, p := range []string{"123456789012", "password1234", "qwertyuiop", "aaaaaaaaaaaa", "111111111111"} {
		if err := validatePassphrase(p); err == nil {
			t.Fatalf("common passphrase %q should be rejected", p)
		}
	}
}

func TestPassphraseAcceptsStrong(t *testing.T) {
	for _, p := range []string{"correct horse battery", "这是一个很安全的同步口令", "aB3xQ9-mK2pL"} {
		if err := validatePassphrase(p); err != nil {
			t.Fatalf("passphrase %q should be accepted: %v", p, err)
		}
	}
}
