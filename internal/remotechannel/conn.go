// Package remotechannel 实现两端之间的加密遥控通道原型。
//
// 目前只允许固定假数据操作，身份与授权仅存在内存；不读取真实会话、
// 不执行命令，私钥不落盘（但也不能据此宣称操作系统不会换出内存）。
package remotechannel

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// FrameLimit 是单条 WebSocket 二进制消息的上限。
const FrameLimit = 16 * 1024

// NewConn 把二进制 WS 消息桥接成标准库 net.Pipe。
//
// 只有读协程触碰 Gorilla 的读状态；net.Pipe 提供同步背压、
// deadline 和并发 Read/Write 语义，不会无限缓存消息。
// 调用方必须把 ws 的独占所有权交给本函数。
func NewConn(ws *websocket.Conn) net.Conn {
	app, pump := net.Pipe()
	var once sync.Once
	var pumps sync.WaitGroup
	closeAll := func() {
		once.Do(func() {
			ws.Close()
			pump.Close()
			app.Close()
		})
	}

	ws.SetReadLimit(FrameLimit)
	window, frames := time.Now(), 0
	rate := func() error {
		if time.Since(window) >= time.Second {
			window = time.Now()
			frames = 0
		}
		frames++
		if frames > 512 {
			return io.ErrNoProgress
		}
		return nil
	}
	// RFC 要求的 pong 用 Gorilla 明确声明并发安全的 WriteControl 回。
	ws.SetPingHandler(func(payload string) error {
		if err := rate(); err != nil {
			return err
		}
		return ws.WriteControl(websocket.PongMessage, []byte(payload), time.Now().Add(time.Second))
	})
	ws.SetPongHandler(func(string) error { return rate() })
	ws.SetCloseHandler(func(int, string) error { return io.EOF })

	pumps.Add(2)
	go func() {
		defer pumps.Done()
		defer closeAll()
		buf := make([]byte, FrameLimit)
		for {
			kind, r, err := ws.NextReader()
			if err != nil || kind != websocket.BinaryMessage || rate() != nil {
				return
			}
			for {
				n, err := r.Read(buf)
				if n > 0 {
					if _, werr := pump.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer pumps.Done()
		defer closeAll()
		buf := make([]byte, FrameLimit)
		for {
			n, err := pump.Read(buf)
			if err != nil {
				return
			}
			ws.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
				return
			}
		}
	}()
	return &streamConn{Conn: app, close: func() {
		closeAll()
		pumps.Wait()
	}}
}

type streamConn struct {
	net.Conn
	close func()
}

func (c *streamConn) Close() error {
	c.close()
	return nil
}
