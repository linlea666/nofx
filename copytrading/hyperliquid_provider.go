package copytrading

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"nofx/market"
)

type hyperliquidProvider struct {
	user         string
	pollInterval time.Duration
	client       *http.Client
	lastTID      int64
	initialized  bool
	lastPositions map[string]float64       // signed size: long >0, short <0
	lastPrices    map[string]float64        // last seen fill price per symbol
}

func newHyperliquidProvider(user string, pollInterval time.Duration, client *http.Client) Provider {
	return &hyperliquidProvider{
		user:         strings.TrimSpace(user),
		pollInterval: pollInterval,
		client:       client,
		lastPositions: make(map[string]float64),
		lastPrices:    make(map[string]float64),
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

	// track latest price per symbol from fills
	maxTID := p.lastTID
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

		symbol := convertHyperliquidSymbol(fill.Coin)
		if symbol == "" {
			p.lastTID = fill.TID
			continue
		}

		price := fill.price()
		if price > 0 {
			p.lastPrices[symbol] = price
		}

		if fill.TID > maxTID {
			maxTID = fill.TID
		}
	}
	if maxTID > p.lastTID {
		p.lastTID = maxTID
	}

	// diff positions: compare current sizes with last snapshot
	if !p.initialized {
		for sym, meta := range state.Positions {
			p.lastPositions[sym] = meta.Size
		}
		p.initialized = true
		return nil
	}

	for sym, meta := range state.Positions {
		prev := p.lastPositions[sym]
		delta := meta.Size - prev
		if delta == 0 {
			continue
		}
		currSym := convertHyperliquidSymbol(sym)
		price := p.lastPrices[currSym]
		if price <= 0 {
			if md, err := market.Get(currSym); err == nil && md.CurrentPrice > 0 {
				price = md.CurrentPrice
				p.lastPrices[currSym] = price
			}
		}
		if price <= 0 {
			// keep snapshot, wait for price next round
			continue
		}
		// handle flip: close prev then open new
		if prev > 0 && meta.Size < 0 {
			out <- Signal{
				Symbol:         currSym,
				Action:         ActionCloseLong,
				NotionalUSD:    math.Abs(prev) * price,
				Price:          price,
				LeaderEquity:   state.AccountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      -prev,
				LeaderPosBefore: prev,
				LeaderPosAfter:  0,
			}
			out <- Signal{
				Symbol:         currSym,
				Action:         ActionOpenShort,
				NotionalUSD:    math.Abs(meta.Size) * price,
				Price:          price,
				LeaderEquity:   state.AccountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      meta.Size,
				LeaderPosBefore: 0,
				LeaderPosAfter:  meta.Size,
			}
			p.lastPositions[sym] = meta.Size
			continue
		}
		if prev < 0 && meta.Size > 0 {
			out <- Signal{
				Symbol:         currSym,
				Action:         ActionCloseShort,
				NotionalUSD:    math.Abs(prev) * price,
				Price:          price,
				LeaderEquity:   state.AccountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      -prev,
				LeaderPosBefore: prev,
				LeaderPosAfter:  0,
			}
			out <- Signal{
				Symbol:         currSym,
				Action:         ActionOpenLong,
				NotionalUSD:    math.Abs(meta.Size) * price,
				Price:          price,
				LeaderEquity:   state.AccountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      meta.Size,
				LeaderPosBefore: 0,
				LeaderPosAfter:  meta.Size,
			}
			p.lastPositions[sym] = meta.Size
			continue
		}

		action := deriveActionFromDelta(prev, meta.Size)
		if action == "" {
			p.lastPositions[sym] = meta.Size
			continue
		}
		s := Signal{
			Symbol:         currSym,
			Action:         action,
			NotionalUSD:    math.Abs(delta) * price,
			Price:          price,
			LeaderEquity:   state.AccountValue,
			LeaderLeverage: meta.Leverage,
			MarginMode:     meta.MarginMode,
			Timestamp:      time.Now(),
			DeltaSize:      delta,
			LeaderPosBefore: prev,
			LeaderPosAfter:  meta.Size,
		}
		out <- s
		p.lastPositions[sym] = meta.Size
	}
	// handle symbols that were closed (now absent)
	for sym, prev := range p.lastPositions {
		if _, ok := state.Positions[sym]; ok {
			continue
		}
		if prev == 0 {
			delete(p.lastPositions, sym)
			continue
		}
		currSym := convertHyperliquidSymbol(sym)
		price := p.lastPrices[currSym]
		if price <= 0 {
			if md, err := market.Get(currSym); err == nil && md.CurrentPrice > 0 {
				price = md.CurrentPrice
				p.lastPrices[currSym] = price
			}
		}
		if price <= 0 {
			delete(p.lastPositions, sym)
			continue
		}
		action := ActionCloseLong
		if prev < 0 {
			action = ActionCloseShort
		}
		s := Signal{
			Symbol:         currSym,
			Action:         action,
			NotionalUSD:    math.Abs(prev) * price,
			Price:          price,
			LeaderEquity:   state.AccountValue,
			LeaderLeverage: 0,
			MarginMode:     "",
			Timestamp:      time.Now(),
			DeltaSize:      -prev,
			LeaderPosBefore: prev,
			LeaderPosAfter:  0,
		}
		out <- s
		delete(p.lastPositions, sym)
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
	Size       float64 // signed size: long>0, short<0
}

type hyperliquidStateRaw struct {
	MarginSummary struct {
		AccountValue string `json:"accountValue"`
	} `json:"marginSummary"`
	AssetPositions []struct {
		Position struct {
			Coin     string `json:"coin"`
			Szi      string `json:"szi"`
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
		size, _ := strconv.ParseFloat(asset.Position.Szi, 64)
		state.Positions[coin] = hyperliquidPositionMeta{
			MarginMode: asset.Position.Leverage.Type,
			Leverage:   lev,
			Size:       size,
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
