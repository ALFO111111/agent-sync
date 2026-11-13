package remotev1

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const HistoryPagedFeature = "history.paged.v1"
const historySnapshotBytes = 64 << 20
const historySnapshotTTL = 10 * time.Minute

type historySnapshot struct {
	token, tool, cwd, session string
	messages                  []HistoryMessage
	expires                   time.Time
	bytes                     int
}

// Connection-local, bounded, immutable snapshots. Cursors contain no file paths.
type historyPager struct{ snapshots []historySnapshot }

func (p *historyPager) page(env Envelope, load func() ([]HistoryMessage, error)) ([]Envelope, error) {
	if len(env.ID) > 128 {
		return nil, errors.New("history: request id too long")
	}
	limit := env.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, errors.New("history: limit must be 1..200")
	}
	now := time.Now()
	live := p.snapshots[:0]
	for _, s := range p.snapshots {
		if now.Before(s.expires) {
			live = append(live, s)
		}
	}
	clear(p.snapshots[len(live):])
	p.snapshots = live
	var snap *historySnapshot
	end := 0
	if env.Cursor == "" {
		msgs, err := load()
		if err != nil {
			return nil, err
		}
		size := 0
		for _, m := range msgs {
			size += len(m.Text) + len(m.Role) + len(m.Time) + 64
		}
		if size > historySnapshotBytes {
			return nil, errors.New("history: snapshot exceeds 64 MiB")
		}
		total := size
		for _, s := range p.snapshots {
			total += s.bytes
		}
		for len(p.snapshots) > 0 && (len(p.snapshots) >= 4 || total > historySnapshotBytes) {
			total -= p.snapshots[0].bytes
			p.snapshots[0] = historySnapshot{}
			p.snapshots = p.snapshots[1:]
		}
		p.snapshots = append(p.snapshots, historySnapshot{token: RandomToken(16), tool: env.Tool, cwd: env.Cwd, session: env.ClaudeSessionID, messages: msgs, expires: now.Add(historySnapshotTTL), bytes: size})
		snap = &p.snapshots[len(p.snapshots)-1]
		end = len(msgs)
	} else {
		token, pos, ok := strings.Cut(env.Cursor, ":")
		n, err := strconv.Atoi(pos)
		if !ok || err != nil || n < 0 {
			return nil, errors.New("history: invalid cursor")
		}
		for i := range p.snapshots {
			if p.snapshots[i].token == token {
				snap = &p.snapshots[i]
				break
			}
		}
		if snap == nil {
			return nil, errors.New("history: snapshot expired; reload latest page")
		}
		if snap.tool != env.Tool || snap.cwd != env.Cwd || snap.session != env.ClaudeSessionID || n > len(snap.messages) {
			return nil, errors.New("history: cursor does not match session")
		}
		end = n
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	msgs := snap.messages[start:end]
	if msgs == nil {
		msgs = []HistoryMessage{}
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	// A page is bounded independently of the snapshot; retain the newest messages.
	for len(data) > 32<<20 && end-start > 1 {
		start++
		data, err = json.Marshal(snap.messages[start:end])
		if err != nil {
			return nil, err
		}
	}
	if len(data) > 32<<20 {
		return nil, errors.New("history: single message exceeds 32 MiB page budget")
	}
	final := Envelope{V: ProtocolVersion, Type: "session.history.chunk", ID: env.ID, Snapshot: snap.token, Done: true, HasMore: start > 0}
	if start > 0 {
		final.Cursor = fmt.Sprintf("%s:%d", snap.token, start)
	}
	return historyChunks(string(data), final)
}

// Split the serialized array, not messages: even a single huge message is safe.
// Budget the actual JSON envelope (including JSON's second escaping layer).
func historyChunks(data string, final Envelope) ([]Envelope, error) {
	var out []Envelope
	for len(data) > 0 {
		env := final
		env.ChunkIndex = len(out)
		lo, hi := 0, len(data)
		if hi > MaxPlain {
			hi = MaxPlain
		}
		for lo < hi {
			mid := (lo + hi + 1) / 2
			env.Data = data[:mid]
			b, err := json.Marshal(env)
			if err != nil {
				return nil, err
			}
			if len(b) <= MaxPlain {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		n := lo
		for n > 0 && n < len(data) && !utf8.RuneStart(data[n]) {
			n--
		}
		if n == 0 {
			return nil, errors.New("history: frame metadata too large")
		}
		env.Data = data[:n]
		if n < len(data) {
			env.Done = false
			env.Cursor = ""
			env.Snapshot = ""
			env.HasMore = false
		}
		out = append(out, env)
		data = data[n:]
	}
	return out, nil
}

func legacyHistory(env Envelope, msgs []HistoryMessage, err error) Envelope {
	resp := Envelope{V: ProtocolVersion, Type: "session.history", ID: env.ID, Messages: msgs}
	if err == nil {
		b, e := json.Marshal(resp)
		err = e
		if len(b) > MaxPlain {
			err = errors.New("history: response too large; negotiate history.paged.v1")
		}
	}
	if err != nil {
		text := err.Error()
		if len(text) > 1024 {
			text = text[:1024]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		resp = Envelope{V: ProtocolVersion, Type: "session.error", ID: env.ID, Error: text}
	}
	if len(resp.ID) > 128 {
		resp.ID = ""
		resp.Error = "history: request id too long"
	}
	return resp
}
