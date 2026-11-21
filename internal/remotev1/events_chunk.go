package remotev1

import "encoding/json"

// EventsChunkedFeature 表示 session.subscribe 的初始历史会按字节分多帧，
// 最后一帧 Done=true；实时事件同样按字节切批。
const EventsChunkedFeature = "session.events.chunked.v1"

// sessionChunkBudget 是每帧事件数组的序列化预算。取 MaxPlain 的一半，
// 给信封字段和加密开销留足余量。
const sessionChunkBudget = MaxPlain / 2

// chunkSessionEvents 把事件按序列化后的字节数切分成多帧。即使调用方传入
// 空列表也返回一帧，保证订阅请求总有一个结束帧。
func chunkSessionEvents(id, sessionKey string, events []SessionEvent) []Envelope {
	newFrame := func() Envelope {
		return Envelope{Type: "session.events", ID: id, SessionKey: sessionKey}
	}
	out := make([]Envelope, 0, 1)
	cur := newFrame()
	size := 0
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		if len(cur.Events) > 0 && size+len(b)+1 > sessionChunkBudget {
			out = append(out, cur)
			cur = newFrame()
			size = 0
		}
		cur.Events = append(cur.Events, ev)
		size += len(b) + 1
	}
	if len(cur.Events) > 0 || len(out) == 0 {
		out = append(out, cur)
	}
	for i := range out {
		out[i].ChunkIndex = i
		out[i].Done = i == len(out)-1
	}
	return out
}
