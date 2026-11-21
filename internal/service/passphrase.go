package service

import (
	"context"
	"errors"
	"strings"

	"github.com/ALFO111111/agent-sync/internal/crypt"
	"github.com/ALFO111111/agent-sync/internal/engine"
	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// 同步口令：修改（所有设备一起换），以及别的设备改过之后本机换上新口令。

const minPassphraseRunes = 12

// commonPassphrases 是最常被撞库的口令；同步口令同时是加密密钥，弱口令等于没加密。
var commonPassphrases = map[string]bool{
	"123456789012":   true,
	"1234567890123":  true,
	"12345678901234": true,
	"1234567890":     true,
	"password":       true,
	"password1234":   true,
	"password123456": true,
	"qwertyuiop":     true,
	"qwerty123456":   true,
	"111111111111":   true,
	"000000000000":   true,
	"aaaaaaaaaaaa":   true,
	"letmein12345":   true,
	"admin1234567":   true,
	"iloveyou1234":   true,
}

func validatePassphrase(p string) error {
	if p == "" {
		return errors.New(i18n.T("口令不能为空"))
	}
	if len([]rune(p)) < minPassphraseRunes {
		return errors.New(i18n.T("口令至少需要 12 个字符"))
	}
	lower := strings.ToLower(p)
	if commonPassphrases[lower] {
		return errors.New(i18n.T("这个口令太常见，换一个更难猜的"))
	}
	// 全部是同一个字符（例如 12 个 a）也属于弱口令。
	first := []rune(p)[0]
	same := true
	for _, r := range p {
		if r != first {
			same = false
			break
		}
	}
	if same {
		return errors.New(i18n.T("这个口令太常见，换一个更难猜的"))
	}
	return nil
}

// ChangePassphrase 修改同步口令：云端全部文件用新口令重新加密（文件多时要几分钟）。
// 改完后其他设备会停下来，提示输入新口令。中途失败可以原样再来一次。
func ChangePassphrase(ctx context.Context, oldPass, newPass string, progress func(done, total int)) error {
	if err := validatePassphrase(newPass); err != nil {
		return err
	}
	return engine.ChangeKey(ctx, crypt.DeriveKey(oldPass, crypt.KeySalt), crypt.DeriveKey(newPass, crypt.KeySalt), progress)
}

// AdoptPassphrase 别的设备改过口令后，在本机输入新口令（会先和云端核对）。
func AdoptPassphrase(ctx context.Context, pass string) error {
	if pass == "" {
		return errors.New(i18n.T("口令不能为空"))
	}
	return engine.AdoptKey(ctx, crypt.DeriveKey(pass, crypt.KeySalt))
}

// PassphraseMatches 本机口令和云端是否一致（不一致 = 别的设备改过口令，本机要用 AdoptPassphrase 换上新的）。
func PassphraseMatches(ctx context.Context) (bool, error) { return engine.KeyMatchesCloud(ctx) }
