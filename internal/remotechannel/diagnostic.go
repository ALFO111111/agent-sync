package remotechannel

// 连接阶段是固定标签，绝不包含对端可控的文字。
const (
	StageRelayDial    = "relay_dial"
	StageRelayPair    = "relay_pair"
	StageTLSConfig    = "tls_config"
	StageTLSHandshake = "tls_handshake"
	StagePairSend     = "pair_send"
	StagePairWait     = "pair_wait"
	StageApprovalWait = "approval_wait"
)

// StageError 保留底层错误用于分类（errors.Is/As），但 Error() 只输出阶段名：
// 网络错误里可能带邀请 URL 或对端控制的文本，不能直接显示。
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	switch e.Stage {
	case StageRelayDial, StageRelayPair, StageTLSConfig, StageTLSHandshake,
		StagePairSend, StagePairWait, StageApprovalWait:
		return e.Stage
	default:
		return "unknown"
	}
}

func (e *StageError) Unwrap() error { return e.Err }

func (c *Client) progress(stage string) {
	if c.OnProgress != nil {
		c.OnProgress(stage)
	}
}
