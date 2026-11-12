// remotepairprobe 是阶段 A 的终端原型：
//
//	host       模拟被遥控的电脑（显示配对邀请，本地批准/撤销）
//	controller 模拟手机（粘贴邀请，等待批准后执行固定假计数）
//	selftest   不交互地跑一遍完整闭环，用于自动验收
//
// 它不读取真实会话、项目文件或模型凭据，不执行命令；身份只在内存里。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ALFO111111/agent-sync/internal/relay"
	"github.com/ALFO111111/agent-sync/internal/remotechannel"
	"golang.org/x/term"
)

var texts = map[string][2]string{
	"help":        {"阶段A终端原型；不读取真实数据，不执行命令，身份仅内存。", "Stage A terminal prototype; no real data, no command execution, in-memory identities only."},
	"terminal":    {"必须使用交互终端；自检模式（-mode selftest）不需要。", "Interactive terminal required; selftest mode (-mode selftest) does not need one."},
	"invite":      {"配对邀请（秘密，仅可信分享）：", "Pairing invitation (secret; trusted sharing only):"},
	"paste":       {"粘贴配对邀请后回车（不回显）：", "Paste pairing invitation and press Enter (hidden input):"},
	"ready":       {"主机连接就绪。命令：invite / pending / approve 请求ID 指纹 / deny 请求ID 指纹 / revoke 指纹 / quit", "Host connected. Commands: invite / pending / approve requestID fingerprint / deny requestID fingerprint / revoke fingerprint / quit"},
	"pending":     {"当前待批准请求（一次性快照，不是实时等待）：", "Pending requests (a snapshot, not a live watch):"},
	"no-pending":  {"没有待批准请求。若控制端还在连接，稍后再执行 pending。", "No pending requests. If the controller is still connecting, run pending again shortly."},
	"connected":   {"已批准。命令：inc / retry / reconnect / quit；每15秒固定状态心跳。", "Approved. Commands: inc / retry / reconnect / quit; fixed status heartbeat every 15s."},
	"waiting":     {"已提交配对请求（请求ID前8位：%s）。请回到主机终端执行 pending，核对完整指纹后 approve。", "Pairing request submitted (request ID prefix: %s). Go to the host terminal, run pending, verify the full fingerprint, then approve."},
	"fingerprint": {"请在主机批准前核对本机完整指纹：", "Verify this complete controller fingerprint on the host before approval:"},
	"failed":      {"操作失败（授权、网络或输入无效）；不会自动重发计数。", "Operation failed (authorization, network or invalid input); counter requests are never auto-replayed."},
	"retry":       {"连接断开，退避后重新认证；不会重发业务请求。", "Disconnected; re-authenticating after backoff; no business replay."},
	"stopped":     {"已退出；本次身份和授权失效。", "Stopped; identities and grants for this run are gone."},
	"value":       {"计数结果 / 请求ID：", "Counter result / request ID:"},
	"stage":       {"安全诊断（阶段 / 分类）：", "Safe diagnosis (stage / class):"},
	"selftest-ok": {"自检通过：配对、批准、计数去重、重连、撤销、旧身份拒绝全部符合预期。", "Selftest passed: pairing, approval, counter dedup, reconnect, revoke and rejected old identity all as expected."},
	"selftest-no": {"自检失败。", "Selftest failed."},
	"invite-hint": {"邀请有效 2 分钟，从主机启动时开始计时；过期请输入 invite 生成新的。", "An invitation is valid for 2 minutes from host start; if expired, type invite for a new one."},
	"arguments":   {"参数无效。", "Invalid options."},
}

var english bool

func text(key string) string {
	n := 0
	if english {
		n = 1
	}
	return texts[key][n]
}

func say(key string) { fmt.Println(text(key)) }

func backoff(ctx context.Context, attempt int) bool {
	if attempt > 5 {
		attempt = 5
	}
	var b [1]byte
	_, _ = rand.Read(b[:])
	d := time.Duration(1<<attempt)*100*time.Millisecond + time.Duration(b[0])*time.Millisecond
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func main() {
	for n, arg := range os.Args {
		if arg == "-lang=en" || (arg == "-lang" && n+1 < len(os.Args) && os.Args[n+1] == "en") {
			english = true
		}
	}
	mode := flag.String("mode", "host", "host / controller / selftest")
	endpoint := flag.String("relay", "wss://relay.example.com/ws", "relay URL")
	local := flag.Bool("local", false, "allow ws only for loopback tests")
	lang := flag.String("lang", "zh-CN", "zh-CN / en")
	duration := flag.Duration("duration", 10*time.Minute, "automatic shutdown (max 1h)")
	flag.Parse()
	english = *lang == "en"
	if (*mode != "host" && *mode != "controller" && *mode != "selftest") || *duration <= 0 || *duration > time.Hour {
		say("arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	if *mode == "selftest" {
		say("help")
		if err := selftest(ctx, *endpoint, *local); err != nil {
			printFailure(err)
			say("selftest-no")
			os.Exit(1)
		}
		say("selftest-ok")
		return
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		say("terminal")
		os.Exit(2)
	}
	say("help")
	var err error
	if *mode == "host" {
		err = runHost(ctx, cancel, *endpoint, *local)
	} else {
		err = runController(ctx, cancel, *local)
	}
	if err != nil {
		printFailure(err)
		say("failed")
	}
	say("stopped")
}

// printFailure 只输出固定阶段和安全分类，绝不回显原始错误（可能含 URL、代理或秘密）。
func printFailure(err error) {
	stage := "none"
	var se *remotechannel.StageError
	if errors.As(err, &se) {
		stage = se.Error()
	}
	class := "other"
	switch {
	case errors.Is(err, context.Canceled):
		class = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		class = "timeout"
	case errors.Is(err, remotechannel.ErrDenied):
		class = "denied"
	case errors.Is(err, remotechannel.ErrIdentity):
		class = "identity"
	case errors.Is(err, relay.ErrProtocol):
		class = "protocol"
	default:
		var ne interface{ Timeout() bool }
		if errors.As(err, &ne) && ne.Timeout() {
			class = "timeout"
		}
	}
	fmt.Println(text("stage"), stage, "/", class)
	debugErr("failure", err)
}

// debugErr 只在本机排查时（CCS_PROBE_DEBUG=1）输出原始错误；
// 正常使用和反馈里绝不显示，避免泄漏 URL、代理或邀请。
func debugErr(label string, err error) {
	if os.Getenv("CCS_PROBE_DEBUG") != "1" || err == nil {
		return
	}
	fmt.Printf("[debug] %s: %v\n", label, err)
}

func lines(ctx context.Context) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		s := bufio.NewScanner(os.Stdin)
		s.Buffer(make([]byte, 1024), 8192)
		for s.Scan() {
			select {
			case out <- s.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func runHost(ctx context.Context, cancel context.CancelFunc, endpoint string, local bool) error {
	h, err := remotechannel.NewHost(endpoint)
	if err != nil {
		return err
	}
	initial, err := h.Invite()
	if err != nil {
		return err
	}
	if err = initial.Validate(local); err != nil {
		return err
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for attempt := 0; ctx.Err() == nil; attempt++ {
			h.Serve(ctx, func() { say("ready") })
			if ctx.Err() != nil {
				return
			}
			say("retry")
			if !backoff(ctx, attempt) {
				return
			}
		}
	}()
	defer func() { cancel(); <-finished }()

	say("invite")
	fmt.Println(remotechannel.EncodeInvitation(initial))
	say("invite-hint")
	input := lines(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-input:
			if !ok {
				return nil
			}
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			err = nil
			switch parts[0] {
			case "quit":
				return nil
			case "invite":
				var i remotechannel.Invitation
				i, err = h.Invite()
				if err == nil {
					say("invite")
					fmt.Println(remotechannel.EncodeInvitation(i))
				}
			case "pending":
				list := h.Authority.Pending()
				if len(list) == 0 {
					say("no-pending")
				} else {
					say("pending")
					for _, p := range list {
						fmt.Println(p.ID, p.Fingerprint, p.Expires.Format(time.RFC3339))
					}
				}
			case "approve", "deny":
				if len(parts) != 3 {
					err = remotechannel.ErrDenied
				} else {
					err = h.Authority.Decide(parts[1], parts[2], parts[0] == "approve")
				}
			case "revoke":
				if len(parts) != 2 {
					err = remotechannel.ErrDenied
				} else {
					err = h.Authority.Revoke(parts[1])
				}
			default:
				err = remotechannel.ErrDenied
			}
			if err != nil {
				say("failed")
			}
		}
	}
}

func secretInput(ctx context.Context) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, state)
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		var b []byte
		one := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(one)
			if err != nil {
				done <- result{nil, err}
				return
			}
			if n == 0 {
				continue
			}
			switch one[0] {
			case '\r', '\n':
				done <- result{b, nil}
				return
			case 8, 127:
				if len(b) > 0 {
					b = b[:len(b)-1]
				}
			default:
				if len(b) >= 8192 {
					done <- result{nil, remotechannel.ErrDenied}
					return
				}
				b = append(b, one[0])
			}
		}
	}()
	select {
	case r := <-done:
		return r.b, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func runController(ctx context.Context, cancel context.CancelFunc, local bool) error {
	say("paste")
	b, err := secretInput(ctx)
	fmt.Println()
	if err != nil {
		return err
	}
	inv, err := remotechannel.DecodeInvitation(b, local)
	for i := range b {
		b[i] = 0
	}
	if err != nil {
		return err
	}
	c, err := remotechannel.NewClient(inv)
	if err != nil {
		return err
	}
	defer c.Close()
	say("fingerprint")
	fmt.Println(c.Identity.Fingerprint)
	c.OnProgress = func(stage string) {
		if stage == remotechannel.StageApprovalWait && c.PendingID != "" {
			fmt.Printf(text("waiting")+"\n", c.PendingID[:8])
		}
	}
	if err = c.Connect(ctx, true); err != nil {
		return err
	}
	say("connected")
	input := lines(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	last := ""
	reconnect := func() error {
		c.Close()
		say("retry")
		var lastErr error
		for attempt := 0; attempt < 6; attempt++ {
			if !backoff(ctx, attempt) {
				return ctx.Err()
			}
			if lastErr = c.Connect(ctx, false); lastErr == nil {
				return nil
			}
		}
		return lastErr
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if c.Status() != nil {
				if err = reconnect(); err != nil {
					return err
				}
			}
		case line, ok := <-input:
			if !ok {
				return nil
			}
			cmd := strings.TrimSpace(line)
			err = nil
			switch cmd {
			case "quit":
				cancel()
				return nil
			case "reconnect":
				err = reconnect()
			case "inc", "retry":
				if cmd == "inc" {
					last = relay.Token()
				}
				if last == "" {
					err = remotechannel.ErrDenied
					break
				}
				var n uint64
				n, err = c.Increment(last)
				if err == nil {
					say("value")
					fmt.Println(n, last)
				} else {
					err = reconnect()
				}
			default:
				err = remotechannel.ErrDenied
			}
			if err != nil {
				say("failed")
			}
		}
	}
}

// selftest 在本进程里同时扮演主机和控制端，通过真实中转跑一遍完整闭环。
// 它不要求交互终端，方便在 Mac/公网入口上自动验收。
func selftest(ctx context.Context, endpoint string, local bool) error {
	start := time.Now()
	mark := func(stage string) {
		fmt.Printf("[%7.2fs] %s\n", time.Since(start).Seconds(), stage)
	}
	mark("start")
	h, err := remotechannel.NewHost(endpoint)
	if err != nil {
		return err
	}
	inv, err := h.Invite()
	if err != nil {
		return err
	}
	if err = inv.Validate(local); err != nil {
		return err
	}
	ready := make(chan struct{})
	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	serveDone := make(chan struct{})
	serveErr := make(chan error, 1)
	go func() {
		defer close(serveDone)
		err := h.Serve(serveCtx, func() { mark("host_ready"); close(ready) })
		if err != nil {
			mark("host_serve_error")
			debugErr("host serve", err)
		}
		serveErr <- err
	}()
	defer func() { stopServe(); <-serveDone }()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		return &remotechannel.StageError{Stage: remotechannel.StageRelayPair, Err: context.DeadlineExceeded}
	case <-ctx.Done():
		return ctx.Err()
	}

	c, err := remotechannel.NewClient(inv)
	if err != nil {
		return err
	}
	defer c.Close()
	c.OnProgress = mark
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx, true) }()
	if err = approveNext(h, c, connectErr, mark); err != nil {
		return err
	}

	id1 := relay.Token()
	if got, err := c.Increment(id1); err != nil || got != 1 {
		return fmt.Errorf("first increment %d %v", got, err)
	}
	if got, err := c.Increment(id1); err != nil || got != 1 {
		return fmt.Errorf("duplicate increment %d %v", got, err)
	}
	if err = c.Status(); err != nil {
		return fmt.Errorf("status before revoke: %w", err)
	}
	if err = c.Connect(ctx, false); err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}
	if got, err := c.Increment(id1); err != nil || got != 1 {
		return fmt.Errorf("dedup after reconnect %d %v", got, err)
	}
	if got, err := c.Increment(relay.Token()); err != nil || got != 2 {
		return fmt.Errorf("second increment %d %v", got, err)
	}

	if err = h.Authority.Revoke(c.Identity.Fingerprint); err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	if err = c.Status(); err == nil {
		return errors.New("revoked client still active")
	}
	if err = c.Connect(ctx, false); err == nil {
		return errors.New("revoked client reconnected")
	}
	inv2, err := h.Invite()
	if err != nil {
		return err
	}
	c.Invitation = inv2
	if err = c.Connect(ctx, true); err == nil {
		return errors.New("revoked identity paired again")
	}

	// 新身份仍可正常配对：撤销只拒绝旧身份，不破坏整个主机。
	c2, err := remotechannel.NewClient(inv2)
	if err != nil {
		return err
	}
	defer c2.Close()
	connectErr2 := make(chan error, 1)
	go func() { connectErr2 <- c2.Connect(ctx, true) }()
	if err = approveNext(h, c2, connectErr2, mark); err != nil {
		return err
	}
	if got, err := c2.Increment(relay.Token()); err != nil || got != 3 {
		return fmt.Errorf("fresh client increment %d %v", got, err)
	}
	return nil
}

// approveNext 等控制端在主机侧出现待批准请求，核对是同一个身份后批准。
func approveNext(h *remotechannel.Host, c *remotechannel.Client, result <-chan error, mark func(string)) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case err := <-result:
			if err == nil {
				return errors.New("connected before approval")
			}
			return fmt.Errorf("connect before approval: %w", err)
		default:
		}
		for _, p := range h.Authority.Pending() {
			if p.Fingerprint != c.Identity.Fingerprint {
				continue
			}
			mark("pending_approved")
			if err := h.Authority.Decide(p.ID, p.Fingerprint, true); err != nil {
				return fmt.Errorf("approve: %w", err)
			}
			select {
			case err := <-result:
				mark("connect_done")
				return err
			case <-time.After(60 * time.Second):
				return errors.New("approval did not complete")
			}
		}
		if time.Now().After(deadline) {
			return errors.New("no pending request appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
