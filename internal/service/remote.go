package service

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/ALFO111111/agent-sync/internal/config"
	"github.com/ALFO111111/agent-sync/internal/fsutil"
	"github.com/ALFO111111/agent-sync/internal/hostbundle"
	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// RemoteStatus 是手机遥控页展示的状态。
type RemoteStatus struct {
	Running        bool            `json:"running"`
	Version        string          `json:"version,omitempty"`
	BuildID        string          `json:"buildID,omitempty"`
	PID            int             `json:"pid,omitempty"`
	BundledVersion string          `json:"bundledVersion,omitempty"`
	BundledBuildID string          `json:"bundledBuildID,omitempty"`
	NeedsRestart   bool            `json:"needsRestart"`
	InstallError   string          `json:"installError,omitempty"`
	HostID         string          `json:"hostId,omitempty"`
	HostName       string          `json:"hostName,omitempty"`
	Relay          string          `json:"relay,omitempty"`
	Pending        []RemotePending `json:"pending,omitempty"`
	Devices        []RemoteDevice  `json:"devices,omitempty"`
	Active         []RemoteActive  `json:"active,omitempty"`
	Dir            string          `json:"dir"`
	BinPath        string          `json:"binPath,omitempty"`
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
	OK         bool            `json:"ok"`
	Error      string          `json:"error,omitempty"`
	Invitation string          `json:"invitation,omitempty"`
	Pending    []RemotePending `json:"pending,omitempty"`
	Devices    []RemoteDevice  `json:"devices,omitempty"`
	Active     []RemoteActive  `json:"active,omitempty"`
	HostID     string          `json:"hostId,omitempty"`
	HostName   string          `json:"hostName,omitempty"`
	Relay      string          `json:"relay,omitempty"`
	Version    string          `json:"version,omitempty"`
	BuildID    string          `json:"buildID,omitempty"`
	PID        int             `json:"pid,omitempty"`
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

// remoteBin 查询路径不执行安装；发布版内置组件优先于开发环境的同目录/PATH。
func remoteBin(cfg *config.Config) string {
	if cfg.RemoteBin != "" {
		return cfg.RemoteBin
	}
	if hostbundle.Available() {
		p, _ := hostbundle.Path()
		return p
	}
	return autoRemoteBin()
}

var remoteLifecycle sync.Mutex
var remoteStopping = map[string]int{} // 持remoteLifecycle；停止超时后也必须等原PID真正消失。
var remoteInstall struct {
	sync.RWMutex
	err error
}

// PrepareRemoteHost 只安装内置组件，不启动遥控、不改变配对信息。
func PrepareRemoteHost() error {
	var err error
	if hostbundle.Available() {
		_, err = hostbundle.Ensure()
	}
	remoteInstall.Lock()
	remoteInstall.err = err
	remoteInstall.Unlock()
	return err
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
	st := RemoteStatus{Dir: dir, BinPath: remoteBin(cfg), BundledVersion: hostbundle.Version(), BundledBuildID: hostbundle.BuildID()}
	remoteInstall.RLock()
	if remoteInstall.err != nil {
		st.InstallError = remoteInstall.err.Error()
	}
	remoteInstall.RUnlock()
	resp, err := remoteCall(dir, "status", 5*time.Second)
	if err != nil {
		return st
	}
	st.Running = true
	st.Version, st.BuildID, st.PID = resp.Version, resp.BuildID, resp.PID
	st.NeedsRestart = cfg.RemoteBin == "" && hostbundle.Available() && (resp.Version != st.BundledVersion || resp.BuildID != st.BundledBuildID || resp.PID <= 0)
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
	return &RemoteConfig{
		Relay:     DisplayRelayURL(cfg.RemoteRelay),
		Public:    DisplayRelayURL(cfg.RemotePublic),
		Name:      cfg.RemoteName,
		ClaudeBin: cfg.RemoteClaudeBin,
		Bin:       cfg.RemoteBin,
		Proxy:     cfg.RemoteProxy,
	}, nil
}

// SaveRemoteConfig 保存设置；Bin 为空时保持自动查找。
func SaveRemoteConfig(rc RemoteConfig) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	relay, err := NormalizeRelayURL(rc.Relay)
	if err != nil {
		return err
	}
	public, err := NormalizeRelayURL(rc.Public)
	if err != nil {
		return err
	}
	if public == "" {
		public = relay
	}
	cfg.RemoteRelay = relay
	cfg.RemotePublic = public
	cfg.RemoteName = strings.TrimSpace(rc.Name)
	cfg.RemoteClaudeBin = strings.TrimSpace(rc.ClaudeBin)
	cfg.RemoteProxy = strings.TrimSpace(rc.Proxy)
	// 空值才代表自动；显式选择的路径应原样保留。
	cfg.RemoteBin = strings.TrimSpace(rc.Bin)
	return config.Save(cfg)
}

// RemoteEnsure 在未运行且已配置中转地址时启动主机组件。
func RemoteEnsure(cfg *config.Config) error {
	remoteLifecycle.Lock()
	defer remoteLifecycle.Unlock()
	return remoteEnsureAt(cfg, RemoteDir())
}

func remoteEnsureAt(cfg *config.Config, dir string) error {
	if pid := remoteStopping[dir]; pid > 0 {
		if err := waitRemoteGone(pid, 10*time.Second); err != nil {
			return err
		}
		delete(remoteStopping, dir)
	}
	if _, err := remoteCall(dir, "status", 5*time.Second); err == nil {
		return nil // 旧主机保持运行，不自动中断会话。
	}
	if cfg.RemoteRelay == "" || cfg.RemotePublic == "" {
		return errors.New("remote relay address is not configured")
	}
	bin := cfg.RemoteBin
	if bin == "" && hostbundle.Available() {
		if err := PrepareRemoteHost(); err != nil {
			return err
		}
		bin, _ = hostbundle.Path()
	} else if bin == "" {
		bin = autoRemoteBin()
	}
	if bin == "" {
		return errors.New("agent-sync-host binary not found")
	}
	// 启动前短暂检查生命周期锁；真正的实例互斥由host持锁保证。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "host.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	locked, err := fsutil.TryLockFile(lock)
	if err != nil {
		lock.Close()
		return err
	}
	if !locked {
		lock.Close()
		return waitRemoteReady(dir, nil, 10*time.Second, cfg.RemoteBin == "" && hostbundle.Available())
	}
	_ = fsutil.UnlockFile(lock)
	_ = lock.Close()
	// 拿得到锁说明上次生命周期已结束；崩溃留下的PID可能已复用，不据此永久阻断。
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
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return waitRemoteReady(dir, exited, 10*time.Second, cfg.RemoteBin == "" && hostbundle.Available())
}

func waitRemoteReady(dir string, exited <-chan error, timeout time.Duration, bundled bool) error {
	check := func() (bool, error) {
		st, err := remoteCall(dir, "status", time.Second)
		if err != nil {
			return false, nil
		}
		if bundled && (st.Version != hostbundle.Version() || st.BuildID != hostbundle.BuildID()) {
			return false, i18n.Err("主机组件版本不匹配，仍未升级，请停止后重启主机；旧版请先重启电脑")
		}
		return true, nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready, err := check(); ready || err != nil {
			return err
		}
		select {
		case <-deadline.C:
			return i18n.Err("主机组件启动超时，请查看 host.log；请勿重复启动")
		case <-exited:
			// 另一个启动者可能已抢先取得锁；只在控制通道已就绪时算成功。
			if ready, err := check(); ready || err != nil {
				return err
			}
			return i18n.Err("主机组件启动失败，请查看 host.log")
		case <-tick.C:
		}
	}
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
	remoteLifecycle.Lock()
	defer remoteLifecycle.Unlock()
	return remoteStopAt(RemoteDir(), 15*time.Second)
}

func remoteStopAt(dir string, timeout time.Duration) error {
	if pid := remoteStopping[dir]; pid > 0 {
		if err := waitRemoteGone(pid, timeout); err != nil {
			return err
		}
		delete(remoteStopping, dir)
	}
	st, err := remoteCall(dir, "status", 5*time.Second)
	if err != nil {
		if raw, readErr := os.ReadFile(filepath.Join(dir, "host.pid")); readErr == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			return waitRemoteGone(pid, timeout)
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		return nil
	}
	if st.PID <= 0 {
		return i18n.Err("旧版主机组件无法确认完全退出，请先重启电脑，再启动手机遥控")
	}
	remoteStopping[dir] = st.PID
	resp, err := remoteDial(dir, remoteRequest{Cmd: "quit"}, 10*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return waitRemoteGone(st.PID, timeout)
}

func waitRemoteGone(pid int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if pid > 0 && fsutil.ProcessGone(pid) {
			return nil
		}
		select {
		case <-deadline.C:
			return i18n.Err("主机组件尚未完全退出，请稍后重试；若仍失败，请重启电脑")
		case <-tick.C:
		}
	}
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

// NormalizeRelayURL 把用户填的地址统一成主机连接用的 WebSocket 地址。
// 支持 https://host、http://host、wss://host、ws://host；没有路径时补 /ws。
func NormalizeRelayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", i18n.Err("中转地址格式不对")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", i18n.Err("中转地址只支持 http/https/ws/wss")
	}
	u.Fragment = ""
	u.RawQuery = ""
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	return u.String(), nil
}

// DisplayRelayURL 把配置里的 wss 地址显示成用户习惯的 https://host 形式。
func DisplayRelayURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return raw
	}
	if u.Path == "/ws" || u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}

// RemoteRemoveDevice 从主机白名单里彻底删除一台设备。
func RemoteRemoveDevice(cfg *config.Config, ref string) error {
	if err := RemoteEnsure(cfg); err != nil {
		return err
	}
	resp, err := remoteDial(RemoteDir(), remoteRequest{Cmd: "remove-device", Ref: ref}, 15*time.Second)
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
