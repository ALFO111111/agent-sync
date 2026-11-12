package service

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/ALFO111111/agent-sync/internal/config"
	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// RemoteStatus 是手机遥控页展示的状态。
type RemoteStatus struct {
	Running  bool         `json:"running"`
	HostID   string       `json:"hostId,omitempty"`
	HostName string       `json:"hostName,omitempty"`
	Relay    string       `json:"relay,omitempty"`
	Pending  []RemotePending `json:"pending,omitempty"`
	Devices  []RemoteDevice  `json:"devices,omitempty"`
	Active   []RemoteActive  `json:"active,omitempty"`
	Dir      string       `json:"dir"`
	BinPath  string       `json:"binPath,omitempty"`
}

type RemotePending struct {
	ID          string `json:"id"`
	DeviceName  string `json:"deviceName"`
	Fingerprint string `json:"fingerprint"`
	Expires     string `json:"expires"` // RFC3339；Go 的 time.Time 在 Wails 绑定里是 any，界面不好用
}

type RemoteDevice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	LastSeen    string `json:"lastSeen,omitempty"` // RFC3339；空 = 从没连过
	Online      bool   `json:"online"`
	Revoked     bool   `json:"revoked,omitempty"`
}

type RemoteActive struct {
	Key      string `json:"key"`
	ClaudeID string `json:"claudeId,omitempty"`
	Cwd      string `json:"cwd"`
	Mode     string `json:"mode"`
	Tool     string `json:"tool,omitempty"`
	Subs     int    `json:"subscribers"`
}

// RemoteConfig 是“手机遥控”的设置项（存在 config.json 里）。
type RemoteConfig struct {
	Relay     string `json:"relay"`     // 主机连接的中转地址，例如 wss://agent-sync.example.com/ws
	Public    string `json:"public"`    // 写给手机的公网地址
	Name      string `json:"name"`      // 电脑显示名
	ClaudeBin string `json:"claudeBin"` // claude 可执行文件（空=自动）
	Bin       string `json:"bin"`       // agent-sync-host 可执行文件（空=自动）
	Proxy     string `json:"proxy"`     // 启动主机组件时用的 HTTP(S) 代理（空=继承当前环境）
}

// RemoteDir 是主机组件的状态目录（与 agent-sync-host 一致）。
func RemoteDir() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "AgentSync", "remote")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "AgentSync", "remote")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-sync", "remote")
}

type remoteRequest struct {
	Token string `json:"token"`
	Cmd   string `json:"cmd"`
	ID    string `json:"id,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Name  string `json:"name,omitempty"`
}

type remoteResponse struct {
	OK         bool           `json:"ok"`
	Error      string         `json:"error,omitempty"`
	Invitation string         `json:"invitation,omitempty"`
	Pending    []RemotePending `json:"pending,omitempty"`
	Devices    []RemoteDevice  `json:"devices,omitempty"`
	Active     []RemoteActive  `json:"active,omitempty"`
	HostID     string         `json:"hostId,omitempty"`
	HostName   string         `json:"hostName,omitempty"`
	Relay      string         `json:"relay,omitempty"`
}

func remoteDial(dir string, req remoteRequest, timeout time.Duration) (remoteResponse, error) {
	var resp remoteResponse
	raw, err := os.ReadFile(filepath.Join(dir, "control"))
	if err != nil {
		return resp, errors.New("remote host is not running")
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return resp, errors.New("remote control file is invalid")
	}
	req.Token = fields[1]
	c, err := net.DialTimeout("tcp", fields[0], 3*time.Second)
	if err != nil {
		return resp, errors.New("remote host is not running")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return resp, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return resp, err
	}
	err = json.Unmarshal(line, &resp)
	return resp, err
}

func remoteCall(dir, cmd string, timeout time.Duration) (remoteResponse, error) {
	resp, err := remoteDial(dir, remoteRequest{Cmd: cmd}, timeout)
	if err != nil {
		return resp, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// remoteBin 找到主机组件可执行文件：配置 > 主程序同目录 > PATH。
func remoteBin(cfg *config.Config) string {
	if cfg.RemoteBin != "" {
		return cfg.RemoteBin
	}
	return autoRemoteBin()
}

// autoRemoteBin 只看主程序同目录和 PATH，不看用户配置。
func autoRemoteBin() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range []string{"agent-sync-host.exe", "agent-sync-host"} {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	if p, err := exec.LookPath("agent-sync-host"); err == nil {
		return p
	}
	return ""
}

// GetRemoteStatus 读取手机遥控状态；没运行时返回 Running=false。
func GetRemoteStatus(cfg *config.Config) RemoteStatus {
	dir := RemoteDir()
	st := RemoteStatus{Dir: dir, BinPath: remoteBin(cfg)}
	resp, err := remoteCall(dir, "status", 5*time.Second)
	if err != nil {
		return st
	}
	st.Running = true
	st.HostID = resp.HostID
	st.HostName = resp.HostName
	st.Relay = resp.Relay
	if list, err := remoteCall(dir, "pending", 5*time.Second); err == nil {
		st.Pending = list.Pending
		for i := range st.Pending {
			st.Pending[i].Expires = zeroToEmpty(st.Pending[i].Expires)
		}
	}
	if list, err := remoteCall(dir, "devices", 5*time.Second); err == nil {
		st.Devices = list.Devices
		for i := range st.Devices {
			st.Devices[i].LastSeen = zeroToEmpty(st.Devices[i].LastSeen)
		}
	}
	if list, err := remoteCall(dir, "sessions", 5*time.Second); err == nil {
		st.Active = list.Active
	}
	return st
}

// GetRemoteConfig 读取“手机遥控”的设置，附带主机组件的路径与状态目录。
func GetRemoteConfig() (*RemoteConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	st := GetRemoteStatus(cfg)
	return &RemoteConfig{
		Relay:     cfg.RemoteRelay,
		Public:    cfg.RemotePublic,
		Name:      cfg.RemoteName,
		ClaudeBin: cfg.RemoteClaudeBin,
		Bin:       st.BinPath,
		Proxy:     cfg.RemoteProxy,
	}, nil
}

// SaveRemoteConfig 保存设置；Bin 为空时保持自动查找。
func SaveRemoteConfig(rc RemoteConfig) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.RemoteRelay = strings.TrimSpace(rc.Relay)
	cfg.RemotePublic = strings.TrimSpace(rc.Public)
	cfg.RemoteName = strings.TrimSpace(rc.Name)
	cfg.RemoteClaudeBin = strings.TrimSpace(rc.ClaudeBin)
	cfg.RemoteProxy = strings.TrimSpace(rc.Proxy)
	// 只在用户填了和自动查找不同的路径时才记下来，避免换机器后指向旧路径。
	if b := strings.TrimSpace(rc.Bin); b != "" && b != autoRemoteBin() {
		cfg.RemoteBin = b
	} else {
		cfg.RemoteBin = ""
	}
	return config.Save(cfg)
}

// RemoteEnsure 在未运行且已配置中转地址时启动主机组件。
func RemoteEnsure(cfg *config.Config) error {
	st := GetRemoteStatus(cfg)
	if st.Running {
		return nil
	}
	if cfg.RemoteRelay == "" || cfg.RemotePublic == "" {
		return errors.New("remote relay address is not configured")
	}
	bin := st.BinPath
	if bin == "" {
		return errors.New("agent-sync-host binary not found")
	}
	dir := RemoteDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "host.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	args := []string{"run", "-relay", cfg.RemoteRelay, "-public", cfg.RemotePublic, "-state", dir, "-lang", i18n.Lang()}
	if cfg.RemoteName != "" {
		args = append(args, "-name", cfg.RemoteName)
	}
	if cfg.RemoteClaudeBin != "" {
		args = append(args, "-claude-bin", cfg.RemoteClaudeBin)
	}
	proxy := cfg.RemoteProxy
	cmd := exec.Command(bin, args...)
	hideWindow(cmd)
	cmd.Env = os.Environ()
	if proxy != "" {
		// 图形界面从桌面启动时可能没有代理环境，主机的公网连接需要它。
		cmd.Env = append(cmd.Env,
			"HTTPS_PROXY="+proxy,
			"HTTP_PROXY="+proxy,
			"NO_PROXY=localhost,127.0.0.1",
		)
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	_ = logf.Close()
	_ = cmd.Process.Release()
	return nil
}

// zeroToEmpty 把 Go 的零值时间（0001-01-01）当成“没有”，免得界面显示成七十几万天前。
func zeroToEmpty(s string) string {
	if strings.HasPrefix(s, "0001-01-01") {
		return ""
	}
	return s
}

// RemoteStart 启动主机组件（已在运行则什么都不做）。
func RemoteStart(cfg *config.Config) error { return RemoteEnsure(cfg) }

// RemoteStop 让主机组件退出；没在运行也算成功。
func RemoteStop() error {
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "quit"}, 10*time.Second)
	if err != nil {
		if strings.Contains(err.Error(), "not running") {
			return nil
		}
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

// RemoteRotateKey 更换共享加密密钥；所有手机需要重新配对。
func RemoteRotateKey(cfg *config.Config) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "rotate-key"}, 15*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

// RemoteInvite 生成一次性配对邀请（JSON）。
func RemoteInvite(cfg *config.Config) (string, error) {
	if err := RemoteEnsure(cfg); err != nil {
		return "", err
	}
	resp, err := remoteCall(RemoteDir(), "invite", 10*time.Second)
	if err != nil {
		return "", err
	}
	return resp.Invitation, nil
}

// RemoteInvitation 是给界面用的配对邀请：二维码（data URL）+ 可复制的原文。
type RemoteInvitation struct {
	QR   string `json:"qr"`
	Text string `json:"text"`
}

// RemoteInviteQR 生成配对邀请并画成 PNG 二维码，桌面界面直接当图片显示。
func RemoteInviteQR(cfg *config.Config) (*RemoteInvitation, error) {
	inv, err := RemoteInvite(cfg)
	if err != nil {
		return nil, err
	}
	code, err := qrcode.New(inv, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	code.DisableBorder = false
	png, err := code.PNG(320)
	if err != nil {
		return nil, err
	}
	return &RemoteInvitation{
		QR:   "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		Text: inv,
	}, nil
}

func RemoteDecide(cfg *config.Config, id string, approve bool) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	cmd := "deny"
	if approve {
		cmd = "approve"
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: cmd, ID: id}, 10*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func RemoteRename(cfg *config.Config, ref, name string) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "rename", Ref: ref, Name: name}, 10*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func RemoteRevoke(cfg *config.Config, ref string) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "revoke", Ref: ref}, 10*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func RemoteStopSession(cfg *config.Config, key string) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "session-stop", Ref: key}, 15*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

// SetRemoteConfig 保存中转地址与显示名。
func SetRemoteConfig(cfg *config.Config, relay, public, name, claudeBin string) error {
	cfg.RemoteRelay = strings.TrimSpace(relay)
	cfg.RemotePublic = strings.TrimSpace(public)
	cfg.RemoteName = strings.TrimSpace(name)
	cfg.RemoteClaudeBin = strings.TrimSpace(claudeBin)
	return config.Save(cfg)
}
