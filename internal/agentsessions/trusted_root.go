package agentsessions

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// trustedRoot resolves aliases only at the configured index boundary. Paths below
// that boundary must be local and contain no symlinks, even links staying inside.
// os.Root additionally confines opens if a component changes after validation.
type trustedRoot struct {
	*os.Root
	path string
}

func openTrustedRoot(path string) (*trustedRoot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	return &trustedRoot{Root: root, path: abs}, nil
}

func (r *trustedRoot) check(rel string) error {
	if !filepath.IsLocal(rel) {
		return errors.New("session path escapes index root")
	}
	part := ""
	for _, name := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		part = filepath.Join(part, name)
		fi, err := r.Lstat(part)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("session symlink rejected")
		}
	}
	return nil
}

func (r *trustedRoot) open(rel string) (*os.File, error) {
	if err := r.check(rel); err != nil {
		return nil, err
	}
	return r.Open(rel)
}

func (r *trustedRoot) openPath(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(r.path, abs)
	if err != nil {
		return nil, err
	}
	return r.open(rel)
}

// SameLocalPath compares local cwd values without folding Unix case or resolving
// client-supplied aliases. It deliberately does not use sync's cross-device rules.
func SameLocalPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
