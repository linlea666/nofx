package copytrading

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type hyperliquidProvider struct {
	user         string
	pollInterval time.Duration
	client       *http.Client
	lastTID      int64
	initialized  bool
}

func newHyperliquidProvider(user string, pollInterval time.Duration, client *http.Client) Provider {
	return &hyperliquidProvider{
		user:         strings.TrimSpace(user),
		pollInterval: pollInterval,
		client:       client,
	}
}

func (p *hyperliquidProvider) Run(stopCh <-chan struct{}, out chan<- Signal) error {
	if p.user == "" {
		return fmt.Errorf("hyperliquid provider requires wallet address")
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		if err := p.fetchAndEmit(out); err != nil {
			log.Printf("⚠️  Hyperliquid provider error: %v", err)
		}

		select {
		case <-stopCh:
			return nil
		case <-ticker.C:
		}
	}
}

func (p *hyperliquidProvider) fetchAndEmit(out chan<- Signal) error {
	fills, err := p.fetchFills()
	if err != nil {
		return err
	}

	state, err := p.fetchState()
	if err != nil {
		return err
	}

	if state.AccountValue <= 0 {
		return fmt.Errorf("invalid Hyperliquid account value")
	}

	sort.Slice(fills, func(i, j int) bool {
		if fills[i].Time == fills[j].Time {
			return fills[i].TID < fills[j].TID
		}
		return fills[i].Time < fills[j].Time
	})

	for _, fill := range fills {
		if fill.TID <= p.lastTID {
			continue
		}

		if !p.initialized {
			p.lastTID = fill.TID
			continue
		}

		action := mapHyperliquidAction(fill.Dir)
		if action == "" {
			p.lastTID = fill.TID
			continue
		}

		symbol := convertHyperliquidSymbol(fill.Coin)
		if symbol == "" {
			p.lastTID = fill.TID
			continue
		}

		notional := fill.price() * fill.size()
		if notional <= 0 {
			p.lastTID = fill.TID
			continue
		}

		posMeta := state.Positions[strings.ToUpper(fill.Coin)]
		s := Signal{
			Symbol:        symbol,
			Action:        action,
			NotionalUSD:   notional,
			LeaderEquity:  state.AccountValue,
			LeaderLeverage: posMeta.Leverage,
			MarginMode:    posMeta.MarginMode,
			Timestamp:     time.UnixMilli(fill.Time),
		}

		out <- s
		p.lastTID = fill.TID
	}

	if !p.initialized {
		p.initialized = true
	}
	return nil
}

func (p *hyperliquidProvider) fetchFills() ([]hyperliquidFill, error) {
	body := map[string]interface{}{
		"type": "userFills",
		"user": p.user,
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "https://api.hyperliquid.xyz/info", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("hyperliquid fills error: %s", resp.Status)
	}

	var fills []hyperliquidFill
	if err := json.NewDecoder(resp.Body).Decode(&fills); err != nil {
		return nil, err
	}
	return fills, nil
}

func (p *hyperliquidProvider) fetchState() (*hyperliquidState, error) {
	body := map[string]interface{}{
		"type": "clearinghouseState",
		"user": p.user,
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "https://api.hyperliquid.xyz/info", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("hyperliquid state error: %s", resp.Status)
	}

	var result hyperliquidStateRaw
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.normalize()
}

type hyperliquidFill struct {
	Coin string  `json:"coin"`
	Dir  string  `json:"dir"`
	Px   string  `json:"px"`
	Sz   string  `json:"sz"`
	Time int64   `json:"time"`
	TID  int64   `json:"tid"`
}

func (f hyperliquidFill) price() float64 {
	value, _ := strconv.ParseFloat(f.Px, 64)
	return value
}

func (f hyperliquidFill) size() float64 {
	value, _ := strconv.ParseFloat(f.Sz, 64)
	return value
}

type hyperliquidState struct {
	AccountValue float64
	Positions    map[string]hyperliquidPositionMeta
}

type hyperliquidPositionMeta struct {
	MarginMode string
	Leverage   int
}

type hyperliquidStateRaw struct {
	MarginSummary struct {
		AccountValue string `json:"accountValue"`
	} `json:"marginSummary"`
	AssetPositions []struct {
		Position struct {
			Coin     string `json:"coin"`
			Leverage struct {
				Type  string  `json:"type"`
				Value float64 `json:"value"`
			} `json:"leverage"`
		} `json:"position"`
	} `json:"assetPositions"`
}

func (s *hyperliquidStateRaw) normalize() (*hyperliquidState, error) {
	accountValue, _ := strconv.ParseFloat(s.MarginSummary.AccountValue, 64)
	state := &hyperliquidState{
		AccountValue: accountValue,
		Positions:    make(map[string]hyperliquidPositionMeta),
	}

	for _, asset := range s.AssetPositions {
		coin := strings.ToUpper(asset.Position.Coin)
		lev := int(asset.Position.Leverage.Value)
		if lev <= 0 {
			lev = 1
		}
		state.Positions[coin] = hyperliquidPositionMeta{
			MarginMode: asset.Position.Leverage.Type,
			Leverage:   lev,
		}
	}

	return state, nil
}

func mapHyperliquidAction(dir string) SignalAction {
	switch strings.ToLower(dir) {
	case "open long":
		return ActionOpenLong
	case "close long":
		return ActionCloseLong
	case "open short":
		return ActionOpenShort
	case "close short":
		return ActionCloseShort
	default:
		return ""
	}
}

func convertHyperliquidSymbol(coin string) string {
	coin = strings.TrimSpace(coin)
	if coin == "" {
		return ""
	}
	coin = strings.ToUpper(coin)
	if strings.HasSuffix(coin, "USDT") {
		return coin
	}
	return coin + "USDT"
}
