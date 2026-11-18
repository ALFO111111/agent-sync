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

// keyFor 返回某台设备的会话密钥；旧设备没有独立密钥时退回全局密钥。
// 只查 Authority，不碰 st.mu，避免配对/回复路径的锁重入。
func (h *Host) keyFor(did string) ContentKey {
	if d, ok := h.Authority.DeviceByRef(did); ok && d.ContentKey != "" {
		if k, err := ParseContentKey(d.ContentKey); err == nil {
			return k
		}
	}
	return h.contentKey()
}

// Revoke 撤销设备：标记撤销、清掉它的密钥和事件流；不影响其他手机。
func (h *Host) Revoke(ref string) (Device, error) {
	dev, err := h.Authority.Revoke(ref)
	if err != nil {
		return Device{}, err
	}
	h.dropDevice(dev)
	return dev, nil
}

// RemoveDevice 删除设备；不轮换全局密钥，其他手机继续正常工作。
func (h *Host) RemoveDevice(ref string) error {
	dev, err := h.Authority.RemoveDevice(ref)
	if err != nil {
		return err
	}
	h.dropDevice(dev)
	return nil
}

func (h *Host) dropDevice(dev Device) {
	h.mu.Lock()
	var st *deviceState
	for did, cand := range h.devices {
		if cand.device.ID == dev.ID || did == dev.Fingerprint {
			st = cand
			delete(h.devices, did)
		}
	}
	h.mu.Unlock()
	if st != nil {
		h.clearSubscriptions(st)
	}
}

func (h *Host) readyEnvelope(deviceID string) Envelope {
	env := Envelope{Type: "ready", DeviceID: deviceID, HostID: h.Authority.State().HostID, HostName: h.Name}
	if h.Sessions != nil {
		env.Features = []string{"sessions", HistoryPagedFeature}
	}
	return env
}
