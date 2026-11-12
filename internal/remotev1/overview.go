package remotev1

// HostOverview 是手机“首页/设置”只读展示的电脑概况。
// 不包含任何密钥、口令、云端凭据或文件内容。
type HostOverview struct {
	HostName   string          `json:"hostName"`
	DeviceName string          `json:"deviceName"`
	System     string          `json:"system"`
	Language   string          `json:"language"`
	DataDir    string          `json:"dataDir"`
	Projects   int             `json:"projects"`
	Sessions   int             `json:"sessions"`
	LastSync   string          `json:"lastSync,omitempty"` // RFC3339；空 = 从未同步
	Stats      OverviewStats   `json:"stats"`
	Tools      []ToolSummary   `json:"tools,omitempty"`
	Content    []ContentToggle `json:"content,omitempty"`
}

type OverviewStats struct {
	Pushed int `json:"pushed"`
	Pulled int `json:"pulled"`
	Merged int `json:"merged"`
	Forked int `json:"forked"`
	Errors int `json:"errors"`
}

type ToolSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Installed    bool   `json:"installed"`
	Projects     int    `json:"projects"`
	SessionCount int    `json:"sessionCount"`
	ContentOn    int    `json:"contentOn"`
	ContentTotal int    `json:"contentTotal"`
}

type ContentToggle struct {
	Tool     string `json:"tool"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
}

// DataSink 由 cmd 层实现：只读概况。nil 表示不支持。
type DataSink interface {
	Overview() (HostOverview, error)
}
