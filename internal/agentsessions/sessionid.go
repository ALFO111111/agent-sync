package agentsessions

import (
	"errors"
	"regexp"
)

// resumeIDPattern 只允许 UUID/随机 token 形态的会话 ID。既挡住 shell 元字符，
// 也挡住以 - 开头、会被 CLI 当成参数的注入。
var resumeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{7,127}$`)

func validResumeID(id string) bool {
	return id == "" || resumeIDPattern.MatchString(id)
}

func checkResumeID(id string) error {
	if !validResumeID(id) {
		return errors.New("invalid resume id")
	}
	return nil
}
