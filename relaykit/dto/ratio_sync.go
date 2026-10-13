package dto

type UpstreamDTO struct {
	ID       int    `json:"id,omitempty"`
	Name     string `json:"name" binding:"required"`
	BaseURL  string `json:"base_url" binding:"required"`
	Endpoint string `json:"endpoint"`
}

type UpstreamRequest struct {
	ChannelIDs []int64       `json:"channel_ids"`
	Upstreams  []UpstreamDTO `json:"upstreams"`
	Timeout    int           `json:"timeout"`
}

// TestResult 上游测试连通性结果
type TestResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// DifferenceItem 差异项
// Current 为本地值，可能为 nil
// Upstreams 为各渠道的上游值，具体数值 / "same" / nil

type DifferenceItem struct {
	Current    any             `json:"current"`
	Upstreams  map[string]any  `json:"upstreams"`
	Confidence map[string]bool `json:"confidence"`
}

type SyncableChannel struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Status  int    `json:"status"`
	Type    int    `json:"type"`
}

// UpstreamPriceGroup is one selectable upstream price (a Sub2API group) that a
// channel can be bound to.
type UpstreamPriceGroup struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Platform           string  `json:"platform,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier"`
	PeakRateEnabled    bool    `json:"peak_rate_enabled,omitempty"`
	PeakRateMultiplier float64 `json:"peak_rate_multiplier,omitempty"`
	ModelCount         int     `json:"model_count"`
}
