package remotev1

import "strings"

// TopicSet 是一台电脑与一台手机之间的主题前缀。
type TopicSet struct {
	UID string
	HID string
	DID string
}

func (t TopicSet) prefix() string { return "u/" + t.UID + "/h/" + t.HID + "/" }

func (t TopicSet) Req() string    { return t.prefix() + "req/" + t.DID }
func (t TopicSet) Evt() string    { return t.prefix() + "evt" }
func (t TopicSet) Up() string     { return t.prefix() + "up/" + t.DID }
func (t TopicSet) Status() string { return t.prefix() + "status" }
func (t TopicSet) Pair() string   { return t.prefix() + "pair/" + t.DID }

// HostSubscriptions 是电脑必须订阅的通配主题。
func (t TopicSet) HostSubscriptions() []string {
	return []string{t.prefix() + "req/+", t.prefix() + "up/+", t.prefix() + "pair/+"}
}

// ParseTopic 解析 u/{uid}/h/{hid}/{kind}[/{did}]。
func ParseTopic(topic string) (kind, uid, hid, did string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "u" || parts[2] != "h" {
		return "", "", "", "", false
	}
	uid, hid, kind = parts[1], parts[3], parts[4]
	if uid == "" || hid == "" || kind == "" {
		return "", "", "", "", false
	}
	switch kind {
	case "evt", "status":
		if len(parts) != 5 {
			return "", "", "", "", false
		}
	case "req", "up", "pair":
		if len(parts) != 6 || parts[5] == "" {
			return "", "", "", "", false
		}
		did = parts[5]
	default:
		return "", "", "", "", false
	}
	return kind, uid, hid, did, true
}

// IsDeviceTopic 判断设备主题是否属于 sender。
func IsDeviceTopic(kind, did, sender string) bool {
	if kind == "evt" || kind == "status" {
		return true
	}
	return did != "" && did == sender
}
