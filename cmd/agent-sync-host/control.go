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

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

// ctlRequest/ctlResponse 是本地控制通道的单行 JSON 协议。
// 通道只监听 127.0.0.1，并用随机令牌 + 0600 端口文件限制本机同用户访问。
type ctlRequest struct {
	Token string `json:"token"`
	Cmd   string `json:"cmd"`
	ID    string `json:"id,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Name  string `json:"name,omitempty"`
}

type pendingDTO struct {
	ID          string    `json:"id"`
	DeviceName  string    `json:"deviceName"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
}

type ctlResponse struct {
	OK         bool                  `json:"ok"`
	Error      string                `json:"error,omitempty"`
	Invitation string                `json:"invitation,omitempty"`
	Pending    []pendingDTO          `json:"pending,omitempty"`
	Devices    []remotev1.DeviceInfo `json:"devices,omitempty"`
	Device     *remotev1.Device      `json:"device,omitempty"`
	Active     []activeDTO           `json:"active,omitempty"`
	HostID     string                `json:"hostId,omitempty"`
	HostName   string                `json:"hostName,omitempty"`
	Relay      string                `json:"relay,omitempty"`
	Version    string                `json:"version,omitempty"`
	BuildID    string                `json:"buildID,omitempty"`
	PID        int                   `json:"pid,omitempty"`
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
	case "status":
		st := d.auth.State()
		return ctlResponse{OK: true, HostID: st.HostID, HostName: d.hostName, Relay: d.relayURL, Version: version, BuildID: buildID, PID: os.Getpid()}
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
