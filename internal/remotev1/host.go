package remotev1

import (
	"sync"
	"time"
)

// Host 是电脑端守护进程的业务状态；传输由 Transport 注入。
type Host struct {
	Identity  *Identity
	Authority *Authority
	Name      string
	Logf      func(format string, args ...any)

	Files    FileSink
	Sessions SessionSink
	Data     DataSink

	Auth DeviceRegistrar // 官方模式登记手机；自建模式为 nil

	mu      sync.Mutex
	devices map[string]*deviceState
	seqs    map[string]uint64

	AttachDir   string
	attachments *attachmentStore
}

// RotateContentKey 更换共享密钥；所有手机需要重新配对。
func (h *Host) RotateContentKey() error {
	return h.Authority.RotateContentKey()
}

func shortFingerprint(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12]
}

func (h *Host) DeviceList() []DeviceInfo {
	online := map[string]bool{}
	h.mu.Lock()
	now := time.Now()
	for _, st := range h.devices {
		if st.device.PublicKey != "" && !st.lastSeen.IsZero() && now.Sub(st.lastSeen) < 75*time.Second {
			online[st.device.PublicKey] = true
		}
	}
	h.mu.Unlock()
	var out []DeviceInfo
	for _, d := range h.Authority.Devices() {
		out = append(out, DeviceInfo{
			ID:          d.ID,
			Name:        d.Name,
			Fingerprint: d.Fingerprint,
			LastSeen:    d.LastSeen,
			Online:      online[d.PublicKey],
			Revoked:     d.Revoked,
		})
	}
	return out
}

// Revoke 撤销设备；MQTT token 到期/重连后无法再连。
func (h *Host) Revoke(ref string) (Device, error) {
	return h.Authority.Revoke(ref)
}

// RemoveDevice 撤销并从白名单里彻底删除。
func (h *Host) RemoveDevice(ref string) error {
	_, err := h.Authority.RemoveDevice(ref)
	return err
}

func (h *Host) readyEnvelope(deviceID string) Envelope {
	env := Envelope{Type: "ready", DeviceID: deviceID, HostID: h.Authority.State().HostID, HostName: h.Name}
	if h.Sessions != nil {
		env.Features = []string{"sessions", HistoryPagedFeature}
	}
	return env
}
