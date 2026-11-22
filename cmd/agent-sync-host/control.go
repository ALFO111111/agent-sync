package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

// ctlRequest/ctlResponse 是本地控制通道的单行 JSON 协议。
// 通道只监听 127.0.0.1，并用随机令牌 + 0600 端口文件限制本机同用户访问。
type ctlRequest struct {
	Token        string `json:"token"`
	Cmd          string `json:"cmd"`
	ID           string `json:"id,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Name         string `json:"name,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	Username     string `json:"username,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

type pendingDTO struct {
	ID          string    `json:"id"`
	DeviceName  string    `json:"deviceName"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
}

type ctlResponse struct {
	OK            bool                  `json:"ok"`
	Error         string                `json:"error,omitempty"`
	Invitation    string                `json:"invitation,omitempty"`
	Pending       []pendingDTO          `json:"pending,omitempty"`
	Devices       []remotev1.DeviceInfo `json:"devices,omitempty"`
	Device        *remotev1.Device      `json:"device,omitempty"`
	Active        []activeDTO           `json:"active,omitempty"`
	HostID        string                `json:"hostId,omitempty"`
	HostName      string                `json:"hostName,omitempty"`
	Broker        string                `json:"broker,omitempty"`
	Mode          string                `json:"mode,omitempty"`
	AccountID     string                `json:"accountId,omitempty"`
	Username      string                `json:"username,omitempty"`
	LoggedIn      bool                  `json:"loggedIn,omitempty"`
	Plan          string                `json:"plan,omitempty"`
	PlanExpiresAt int64                 `json:"planExpiresAt,omitempty"`
	UsedBytes     int64                 `json:"usedBytes,omitempty"`
	QuotaBytes    int64                 `json:"quotaBytes,omitempty"`
	BrokerState   string                `json:"brokerState,omitempty"`
	LastError     string                `json:"lastError,omitempty"`
	Version       string                `json:"version,omitempty"`
	BuildID       string                `json:"buildID,omitempty"`
	PID           int                   `json:"pid,omitempty"`
}

func controlFile(dir string) string { return filepath.Join(dir, "control") }

// serveControl 启动本地管理通道，把 "addr token" 写进 0600 文件。
func serveControl(ctx context.Context, dir string, app *daemon) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	if err := writeFileAtomic(controlFile(dir), []byte(addr+" "+app.controlToken+"\n"), 0o600); err != nil {
		ln.Close()
		return err
	}
	closed := make(chan struct{})
	defer close(closed)
	defer os.Remove(controlFile(dir))
	defer ln.Close()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-closed:
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			handleControlConn(c, app)
		}()
	}
}

func handleControlConn(c net.Conn, app *daemon) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, 8*1024)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req ctlRequest
	if json.Unmarshal(line, &req) != nil || req.Token != app.controlToken {
		writeControl(c, ctlResponse{Error: "unauthorized"})
		return
	}
	writeControl(c, app.handle(req))
}

func writeControl(c net.Conn, resp ctlResponse) {
	b, _ := json.Marshal(resp)
	_, _ = c.Write(append(b, '\n'))
}

func (d *daemon) handle(req ctlRequest) ctlResponse {
	switch req.Cmd {
	case "invite":
		inv, err := d.auth.Invite(d.hostName)
		if err != nil {
			return ctlResponse{Error: err.Error()}
		}
		inv.Mode = d.mode
		if d.mode == "selfhost" {
			// 自建没有 auth 服务，二维码里直接带 Mosquitto 的用户名密码，
			// 手机扫码/粘贴后即可连接。二维码只在电脑屏幕上短时间展示。
			inv.MQTTUser, inv.MQTTPassword = d.mqttUser, d.mqttPass
		} else {
			// 官方模式的二维码必须带短期配对凭据，否则手机扫码后会误报“请先登录”。
			st := d.auth.State()
			ac := d.getAuth()
			if ac == nil || st.AccountID == "" || st.RefreshToken == "" {
				return ctlResponse{Error: i18n.T("官方账号未登录，请先在电脑上登录后再生成二维码")}
			}
			inv.Username = st.Username
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			user, pass, err := ac.PairToken(ctx, inv.HostID)
			cancel()
			if err != nil {
				return ctlResponse{Error: i18n.T("账号登录已失效，请重新登录官方账号后再生成二维码")}
			}
			inv.MQTTUser, inv.MQTTPassword = user, pass
		}
		return ctlResponse{OK: true, Invitation: remotev1.EncodeInvitation(inv), HostName: d.hostName}
	case "pending":
		list := d.auth.PendingList()
		out := make([]pendingDTO, 0, len(list))
		for _, p := range list {
			out = append(out, pendingDTO{ID: p.ID, DeviceName: p.DeviceName, Fingerprint: p.Fingerprint, Expires: p.Expires})
		}
		return ctlResponse{OK: true, Pending: out}
	case "approve", "deny":
		if req.ID == "" {
			return ctlResponse{Error: "missing id"}
		}
		if err := d.auth.Decide(req.ID, req.Cmd == "approve"); err != nil {
			return ctlResponse{Error: err.Error()}
		}
		return ctlResponse{OK: true}
	case "devices":
		return ctlResponse{OK: true, Devices: d.host.DeviceList()}
	case "revoke":
		if req.Ref == "" {
			return ctlResponse{Error: "missing ref"}
		}
		dev, err := d.host.Revoke(req.Ref)
		if err != nil {
			return ctlResponse{Error: err.Error()}
		}
		return ctlResponse{OK: true, Device: &dev}
	case "remove-device":
		if req.Ref == "" {
			return ctlResponse{Error: "missing ref"}
		}
		if err := d.host.RemoveDevice(req.Ref); err != nil {
			return ctlResponse{Error: err.Error()}
		}
		return ctlResponse{OK: true}
	case "rename":
		if req.Ref == "" || req.Name == "" {
			return ctlResponse{Error: "missing ref or name"}
		}
		dev, err := d.auth.Rename(req.Ref, req.Name)
		if err != nil {
			return ctlResponse{Error: err.Error()}
		}
		return ctlResponse{OK: true, Device: &dev}
	case "sessions":
		if d.sessions == nil {
			return ctlResponse{Error: "sessions unsupported"}
		}
		list := d.sessions.Active()
		out := make([]activeDTO, 0, len(list))
		for _, a := range list {
			out = append(out, activeDTO{Key: a.Key, ClaudeID: a.ClaudeID, Cwd: a.Cwd, Mode: a.Mode, Subscribers: a.Subscribers, Tool: a.Tool})
		}
		return ctlResponse{OK: true, Active: out}
	case "session-stop":
		if d.sessions == nil || req.Ref == "" {
			return ctlResponse{Error: "missing ref"}
		}
		if !d.sessions.StopRef(req.Ref) {
			return ctlResponse{Error: "session not found"}
		}
		return ctlResponse{OK: true}
	case "account":
		if req.AccountID == "" || req.RefreshToken == "" {
			return ctlResponse{Error: "missing account"}
		}
		if err := d.auth.SetAccount(req.AccountID, req.Username, req.RefreshToken); err != nil {
			return ctlResponse{Error: err.Error()}
		}
		d.reconnect() // 立刻用新账号重建 MQTT 链路
		return ctlResponse{OK: true}
	case "logout":
		if err := d.auth.Logout(); err != nil {
			return ctlResponse{Error: err.Error()}
		}
		d.reconnect() // 断开旧链路；主循环会清空 Auth 并等待重新登录
		return ctlResponse{OK: true}
	case "authstatus":
		st := d.auth.State()
		return ctlResponse{OK: true, AccountID: st.AccountID, Username: st.Username, LoggedIn: st.AccountID != "" && st.RefreshToken != ""}
	case "status":
		st := d.auth.State()
		resp := ctlResponse{OK: true, HostID: st.HostID, HostName: d.hostName, Broker: d.broker, Mode: d.mode, AccountID: st.AccountID, Username: st.Username, LoggedIn: st.AccountID != "" && st.RefreshToken != "", Version: version, BuildID: buildID, PID: os.Getpid()}
		resp.BrokerState, resp.LastError = d.brokerInfo()
		if ac := d.getAuth(); ac != nil && resp.LoggedIn && st.AccountID != "" {
			plan, exp, used, quota, ok := d.cachedUsage(ac)
			if ok {
				resp.Plan, resp.PlanExpiresAt, resp.UsedBytes, resp.QuotaBytes = plan, exp, used, quota
			}
		}
		return resp
	case "rotate-key":
		if err := d.host.RotateContentKey(); err != nil {
			return ctlResponse{Error: err.Error()}
		}
		return ctlResponse{OK: true}
	case "quit":
		d.stop()
		return ctlResponse{OK: true}
	default:
		return ctlResponse{Error: "unknown command"}
	}
}

// cachedUsage 缓存套餐/流量 60 秒：桌面端每 3 秒拉一次状态，
// 每次都打公网会把 auth 的 /v1/me 和 /v1/usage 打爆。
func (d *daemon) cachedUsage(ac remotev1.DeviceRegistrar) (plan string, exp, used, quota int64, ok bool) {
	read := func() (string, int64, int64, int64, bool) {
		d.usageMu.Lock()
		defer d.usageMu.Unlock()
		if time.Since(d.usageAt) < time.Minute {
			return d.usagePlan, d.usageExp, d.usageUsed, d.usageQuota, true
		}
		return "", 0, 0, 0, false
	}
	if plan, exp, used, quota, ok = read(); ok {
		return
	}
	d.usageFetchMu.Lock()
	defer d.usageFetchMu.Unlock()
	if plan, exp, used, quota, ok = read(); ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	plan, exp, used, quota, err := ac.AccountUsage(ctx)
	cancel()
	if err != nil {
		return "", 0, 0, 0, false
	}
	d.usageMu.Lock()
	d.usageAt = time.Now()
	d.usagePlan, d.usageExp, d.usageUsed, d.usageQuota = plan, exp, used, quota
	d.usageMu.Unlock()
	return plan, exp, used, quota, true
}

// sendControl 是 CLI 侧的一次请求。
func sendControl(dir string, req ctlRequest) (ctlResponse, error) {
	var resp ctlResponse
	b, err := os.ReadFile(controlFile(dir))
	if err != nil {
		return resp, errors.New("daemon is not running")
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		return resp, errors.New("control file is invalid")
	}
	req.Token = fields[1]
	c, err := net.DialTimeout("tcp", fields[0], 3*time.Second)
	if err != nil {
		return resp, errors.New("daemon is not running")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	payload, _ := json.Marshal(req)
	if _, err := c.Write(append(payload, '\n')); err != nil {
		return resp, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return resp, err
	}
	err = json.Unmarshal(line, &resp)
	return resp, err
}

type activeDTO struct {
	Key         string `json:"key"`
	ClaudeID    string `json:"claudeId,omitempty"`
	Cwd         string `json:"cwd"`
	Mode        string `json:"mode"`
	Subscribers int    `json:"subscribers"`
	Tool        string `json:"tool,omitempty"`
}
