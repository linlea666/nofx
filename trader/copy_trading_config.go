package trader

import (
	"encoding/json"
	"strings"
)

// CopyTradingConfig 描述前端配置的定比跟单参数
type CopyTradingConfig struct {
	FollowOpen     bool    `json:"follow_open"`
	FollowAdd      bool    `json:"follow_add"`
	FollowReduce   bool    `json:"follow_reduce"`
	FollowRatio    float64 `json:"follow_ratio"`
	MinAmount      float64 `json:"min_amount"`
	MaxAmount      float64 `json:"max_amount"`
	SyncLeverage   bool    `json:"sync_leverage"`
	SyncMarginMode bool    `json:"sync_margin_mode"`
}

// DefaultCopyTradingConfig 返回默认参数
func DefaultCopyTradingConfig() CopyTradingConfig {
	return CopyTradingConfig{
		FollowOpen:     true,
		FollowAdd:      true,
		FollowReduce:   true,
		FollowRatio:    100,
		MinAmount:      0,
		MaxAmount:      0,
		SyncLeverage:   true,
		SyncMarginMode: true,
	}
}

// ParseCopyTradingConfig 解析数据库中的JSON，无法解析时返回默认值
func ParseCopyTradingConfig(raw string) CopyTradingConfig {
	cfg := DefaultCopyTradingConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg
	}
	return normalizeCopyTradingConfig(cfg)
}

func normalizeCopyTradingConfig(cfg CopyTradingConfig) CopyTradingConfig {
	defaultCfg := DefaultCopyTradingConfig()
	if cfg.FollowRatio <= 0 {
		cfg.FollowRatio = defaultCfg.FollowRatio
	}
	if cfg.MaxAmount < 0 {
		cfg.MaxAmount = 0
	}
	if cfg.MinAmount < 0 {
		cfg.MinAmount = 0
	}
	if !cfg.FollowOpen && !cfg.FollowAdd && !cfg.FollowReduce {
		cfg.FollowOpen = defaultCfg.FollowOpen
		cfg.FollowAdd = defaultCfg.FollowAdd
		cfg.FollowReduce = defaultCfg.FollowReduce
	}
	return cfg
}
