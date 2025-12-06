package copytrading

import (
	"errors"
	"net/http"
	"time"
)

// SignalAction represents the normalized action type emitted by signal providers.
type SignalAction string

const (
	ActionOpenLong  SignalAction = "open_long"
	ActionOpenShort SignalAction = "open_short"
	ActionCloseLong SignalAction = "close_long"
	ActionCloseShort SignalAction = "close_short"
	ActionAddLong   SignalAction = "add_long"    // treated as open_long with delta
	ActionAddShort  SignalAction = "add_short"   // treated as open_short with delta
	ActionReduceLong SignalAction = "reduce_long" // treated as close_long with delta
	ActionReduceShort SignalAction = "reduce_short"
)

// Signal is the normalized structure describing a leader's fill event.
type Signal struct {
	Symbol        string
	Action        SignalAction
	NotionalUSD   float64   // Absolute fill size in USD
	LeaderEquity  float64   // Leader account equity at the moment of fill
	LeaderLeverage int
	MarginMode    string    // "cross" or "isolated"
	Timestamp     time.Time
	// For proportional reduce/close:
	DeltaSize        float64 // leader position change size (signed)
	LeaderPosBefore  float64 // leader position size before this change (signed)
	LeaderPosAfter   float64 // leader position size after this change (signed)
}

// Provider defines the behaviour for any external signal source.
type Provider interface {
	Run(stopCh <-chan struct{}, out chan<- Signal) error
}

// Config contains shared initialization parameters for all providers.
type Config struct {
	Type         string
	Identifier   string
	PollInterval time.Duration
	HTTPClient   *http.Client
}

// NewProvider constructs the correct Provider implementation based on the type field.
func NewProvider(cfg Config) (Provider, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	switch cfg.Type {
	case "hyperliquid_wallet", "hyperliquid":
		return newHyperliquidProvider(cfg.Identifier, cfg.PollInterval, cfg.HTTPClient), nil
	case "okx_wallet", "okx":
		return newOKXProvider(cfg.Identifier, cfg.PollInterval, cfg.HTTPClient), nil
	default:
		return nil, errors.New("unsupported signal source type")
	}
}

// deriveActionFromDelta determines action based on previous and current position size (signed).
// Caller should handle direction flip separately if needed.
func deriveActionFromDelta(prev, curr float64) SignalAction {
	delta := curr - prev
	if delta == 0 {
		return ""
	}
	if curr > prev { // moving towards long (increase long or reduce short)
		if curr > 0 {
			return ActionAddLong
		}
		return ActionReduceShort
	}
	if curr < prev { // moving towards short (increase short or reduce long)
		if curr < 0 {
			return ActionAddShort
		}
		return ActionReduceLong
	}
	return ""
}
