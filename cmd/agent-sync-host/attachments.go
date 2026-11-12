package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ALFO111111/agent-sync/internal/agentsessions"
	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const (
	maxInlineText  = 128 << 10
	maxTotalInline = 512 << 10
	maxImageBytes  = 5 << 20
)

// prepareAttachments 把对话级附件变成这一轮消息的一部分：
// 图片给 Claude 内联图片块；文本内联成文本块；其他文件落到会话临时目录并给出路径。
func (s *sessionService) prepareAttachments(key string, atts []remotev1.Attachment, inlineImages bool) (string, []agentsessions.ImageAttachment, error) {
	var extra strings.Builder
	var images []agentsessions.ImageAttachment
	inlineLeft := maxTotalInline
	for _, a := range atts {
		switch a.Kind {
		case "image":
			if !strings.HasPrefix(a.MediaType, "image/") {
				return "", nil, errors.New("invalid image media type")
			}
			if len(a.Data) > maxImageBytes {
				return "", nil, errors.New("image exceeds 5 MB")
			}
			if inlineImages {
				images = append(images, agentsessions.ImageAttachment{MediaType: a.MediaType, Data: a.Data})
				continue
			}
			p, err := s.stageAttachment(key, a.Name, a.Data)
			if err != nil {
				return "", nil, err
			}
			extra.WriteString(i18n.T("\n\n[附件] %s 已保存到：%s（请用 Read 读取）", a.Name, p))
		case "text":
			content := string(a.Data)
			if len(content) > maxInlineText {
				content = content[:maxInlineText] + i18n.T("\n…（已截断）")
			}
			if len(content) > inlineLeft {
				content = content[:inlineLeft] + i18n.T("\n…（已截断）")
			}
			inlineLeft -= len(content)
			extra.WriteString(i18n.T("\n\n【附件 %s】\n```\n%s\n```", a.Name, content))
		case "binary":
			p, err := s.stageAttachment(key, a.Name, a.Data)
			if err != nil {
				return "", nil, err
			}
			extra.WriteString(i18n.T("\n\n[附件] %s 已保存到：%s（请用 Read 读取）", a.Name, p))
		default:
			return "", nil, errors.New("invalid attachment kind")
		}
	}
	return extra.String(), images, nil
}

// stageAttachment 写到一个新的临时目录；目录记在会话名下，会话结束时删除。
func (s *sessionService) stageAttachment(key, name string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "agent-sync-attach-*")
	if err != nil {
		return "", err
	}
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		name = "attachment"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	s.attachMu.Lock()
	if s.attachDirs == nil {
		s.attachDirs = map[string][]string{}
	}
	s.attachDirs[key] = append(s.attachDirs[key], dir)
	s.attachMu.Unlock()
	return p, nil
}

func (s *sessionService) cleanupAttachments(key string) {
	s.attachMu.Lock()
	dirs := s.attachDirs[key]
	delete(s.attachDirs, key)
	s.attachMu.Unlock()
	for _, d := range dirs {
		os.RemoveAll(d)
	}
}

func (s *sessionService) cleanupAllAttachments() {
	s.attachMu.Lock()
	dirs := s.attachDirs
	s.attachDirs = nil
	s.attachMu.Unlock()
	for _, list := range dirs {
		for _, d := range list {
			os.RemoveAll(d)
		}
	}
}
