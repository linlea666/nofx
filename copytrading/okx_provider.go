package copytrading

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"nofx/market"
)

type okxProvider struct {
	uniqueName   string
	pollInterval time.Duration
	client       *http.Client
	lastFillTime int64
	initialized  bool
	lastPositions map[string]float64       // signed size
	lastPrices    map[string]float64       // last fill price per symbol
}

func newOKXProvider(uniqueName string, pollInterval time.Duration, client *http.Client) Provider {
	return &okxProvider{
		uniqueName:   strings.TrimSpace(uniqueName),
		pollInterval: pollInterval,
		client:       client,
		lastPositions: make(map[string]float64),
		lastPrices:    make(map[string]float64),
	}
}

func (p *okxProvider) Run(stopCh <-chan struct{}, out chan<- Signal) error {
	if p.uniqueName == "" {
		return fmt.Errorf("okx provider requires uniqueName")
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		if err := p.fetchAndEmit(out); err != nil {
			log.Printf("⚠️  OKX provider error: %v", err)
		}

		select {
		case <-stopCh:
			return nil
		case <-ticker.C:
		}
	}
}

func (p *okxProvider) fetchAndEmit(out chan<- Signal) error {
	trades, err := p.fetchTrades()
	if err != nil {
		return err
	}

	accountValue, err := p.fetchEquity()
	if err != nil {
		return err
	}
	if accountValue <= 0 {
		return fmt.Errorf("okx equity invalid")
	}

	positions, err := p.fetchPositions()
	if err != nil {
		return err
	}

	sort.Slice(trades, func(i, j int) bool {
		if trades[i].FillTime == trades[j].FillTime {
			return trades[i].OrdID < trades[j].OrdID
		}
		return trades[i].FillTime < trades[j].FillTime
	})

	maxFill := p.lastFillTime
	for _, trade := range trades {
		if trade.FillTime <= p.lastFillTime {
			continue
		}

		symbol := formatOKXSymbol(trade.InstID)
		if symbol == "" {
			continue
		}

		avgPx, _ := strconv.ParseFloat(trade.AvgPx, 64)
		if avgPx > 0 {
			p.lastPrices[symbol] = avgPx
		}
		if trade.FillTime > maxFill {
			maxFill = trade.FillTime
		}
	}
	if maxFill > p.lastFillTime {
		p.lastFillTime = maxFill
	}

	// initialize snapshot without emitting historical signals
	if !p.initialized {
		for sym, meta := range positions {
			p.lastPositions[sym] = meta.Size
		}
		p.initialized = true
		return nil
	}

	for sym, meta := range positions {
		prev := p.lastPositions[sym]
		delta := meta.Size - prev
		if delta == 0 {
			continue
		}
		price := p.lastPrices[sym]
		if price <= 0 {
			if md, err := market.Get(sym); err == nil && md.CurrentPrice > 0 {
				price = md.CurrentPrice
				p.lastPrices[sym] = price
			}
		}
		if price <= 0 {
			continue
		}
		// direction flip
		if prev > 0 && meta.Size < 0 {
			out <- Signal{
				Symbol:         sym,
				Action:         ActionCloseLong,
				NotionalUSD:    math.Abs(prev) * price,
				Price:          price,
				LeaderEquity:   accountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      -prev,
				LeaderPosBefore: prev,
				LeaderPosAfter:  0,
			}
			out <- Signal{
				Symbol:         sym,
				Action:         ActionOpenShort,
				NotionalUSD:    math.Abs(meta.Size) * price,
				Price:          price,
				LeaderEquity:   accountValue,
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
				Symbol:         sym,
				Action:         ActionCloseShort,
				NotionalUSD:    math.Abs(prev) * price,
				Price:          price,
				LeaderEquity:   accountValue,
				LeaderLeverage: meta.Leverage,
				MarginMode:     meta.MarginMode,
				Timestamp:      time.Now(),
				DeltaSize:      -prev,
				LeaderPosBefore: prev,
				LeaderPosAfter:  0,
			}
			out <- Signal{
				Symbol:         sym,
				Action:         ActionOpenLong,
				NotionalUSD:    math.Abs(meta.Size) * price,
				Price:          price,
				LeaderEquity:   accountValue,
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
	out <- Signal{
		Symbol:         sym,
		Action:         action,
		NotionalUSD:    math.Abs(delta) * price,
		Price:          price,
		LeaderEquity:   accountValue,
		LeaderLeverage: meta.Leverage,
		MarginMode:     meta.MarginMode,
		Timestamp:      time.Now(),
		DeltaSize:      delta,
			LeaderPosBefore: prev,
			LeaderPosAfter:  meta.Size,
		}
		p.lastPositions[sym] = meta.Size
	}

	// handle symbols that disappeared -> full close
	for sym, prev := range p.lastPositions {
		if _, ok := positions[sym]; ok {
			continue
		}
		if prev == 0 {
			delete(p.lastPositions, sym)
			continue
		}
		price := p.lastPrices[sym]
		if price <= 0 {
			if md, err := market.Get(sym); err == nil && md.CurrentPrice > 0 {
				price = md.CurrentPrice
				p.lastPrices[sym] = price
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
		out <- Signal{
			Symbol:         sym,
			Action:         action,
			NotionalUSD:    math.Abs(prev) * price,
			LeaderEquity:   accountValue,
			LeaderLeverage: 0,
			MarginMode:     "",
			Timestamp:      time.Now(),
			DeltaSize:      -prev,
			LeaderPosBefore: prev,
			LeaderPosAfter:  0,
		}
		delete(p.lastPositions, sym)
	}

	return nil
}

func (p *okxProvider) fetchTrades() ([]okxTradeRecord, error) {
	params := url.Values{}
	params.Set("uniqueName", p.uniqueName)
	params.Set("instType", "SWAP")
	params.Set("limit", "50")
	params.Set("t", fmt.Sprintf("%d", time.Now().UnixMilli()))
	endpoint := fmt.Sprintf("https://www.okx.com/priapi/v5/ecotrade/public/community/user/trade-records?%s", params.Encode())

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("okx trades error: %s", resp.Status)
	}

	var result okxTradeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Data, nil
}

func (p *okxProvider) fetchEquity() (float64, error) {
	params := url.Values{}
	params.Set("uniqueName", p.uniqueName)
	params.Set("t", fmt.Sprintf("%d", time.Now().UnixMilli()))
	endpoint := fmt.Sprintf("https://www.okx.com/priapi/v5/ecotrade/public/community/user/asset?%s", params.Encode())

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("okx asset error: %s", resp.Status)
	}

	var result okxAssetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	for _, asset := range result.Data {
		if strings.EqualFold(asset.Currency, "USDT") {
			value, _ := strconv.ParseFloat(asset.Amount, 64)
			return value, nil
		}
	}

	return 0, fmt.Errorf("okx equity not found")
}

func (p *okxProvider) fetchMarginModes() (map[string]string, error) {
	params := url.Values{}
	params.Set("uniqueName", p.uniqueName)
	params.Set("t", fmt.Sprintf("%d", time.Now().UnixMilli()))
	endpoint := fmt.Sprintf("https://www.okx.com/priapi/v5/ecotrade/public/community/user/position-current?%s", params.Encode())

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("okx position error: %s", resp.Status)
	}

	var result okxPositionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	positions := make(map[string]string)
	for _, entry := range result.Data {
		for _, pos := range entry.PosData {
			positions[pos.InstID] = strings.ToLower(pos.MarginMode)
		}
	}
	return positions, nil
}

type okxTradeResponse struct {
	Code string            `json:"code"`
	Data []okxTradeRecord  `json:"data"`
	Msg  string            `json:"msg"`
}

type okxTradeRecord struct {
	InstID   string `json:"instId"`
	Side     string `json:"side"`
	PosSide  string `json:"posSide"`
	AvgPx    string `json:"avgPx"`
	Size     string `json:"sz"`
	Value    string `json:"value"`
	FillTime int64  `json:"fillTime,string"`
	OrdID    string `json:"ordId"`
	Lever    string `json:"lever"`
}

type okxAssetResponse struct {
	Code string        `json:"code"`
	Data []okxAssetRow `json:"data"`
	Msg  string        `json:"msg"`
}

type okxAssetRow struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type okxPositionResponse struct {
	Code string              `json:"code"`
	Data []okxPositionParent `json:"data"`
	Msg  string              `json:"msg"`
}

type okxPositionParent struct {
	PosData []okxPositionEntry `json:"posData"`
}

type okxPositionEntry struct {
	InstID     string `json:"instId"`
	MarginMode string `json:"mgnMode"`
	PosSide    string `json:"posSide"`
	Pos        string `json:"pos"`
	Lever      string `json:"lever"`
}

func mapOKXAction(posSide, side string) SignalAction {
	posSide = strings.ToLower(posSide)
	side = strings.ToLower(side)

	switch {
	case posSide == "long" && side == "buy":
		return ActionOpenLong
	case posSide == "long" && side == "sell":
		return ActionCloseLong
	case posSide == "short" && side == "sell":
		return ActionOpenShort
	case posSide == "short" && side == "buy":
		return ActionCloseShort
	default:
		return ""
	}
}

func formatOKXSymbol(instID string) string {
	instID = strings.TrimSpace(instID)
	if instID == "" {
		return ""
	}
	instID = strings.ToUpper(instID)
	instID = strings.ReplaceAll(instID, "-SWAP", "")
	instID = strings.ReplaceAll(instID, "-", "")
	return instID
}

type okxPositionMeta struct {
	Size       float64
	Leverage   int
	MarginMode string
}

func (p *okxProvider) fetchPositions() (map[string]okxPositionMeta, error) {
	params := url.Values{}
	params.Set("uniqueName", p.uniqueName)
	params.Set("t", fmt.Sprintf("%d", time.Now().UnixMilli()))
	endpoint := fmt.Sprintf("https://www.okx.com/priapi/v5/ecotrade/public/community/user/position-current?%s", params.Encode())

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("okx position error: %s", resp.Status)
	}

	var result okxPositionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	positions := make(map[string]okxPositionMeta)
	for _, entry := range result.Data {
		for _, pos := range entry.PosData {
			symbol := formatOKXSymbol(pos.InstID)
			if symbol == "" {
				continue
			}
			size, _ := strconv.ParseFloat(pos.Pos, 64)
			lever, _ := strconv.ParseFloat(pos.Lever, 64)
			if lever <= 0 {
				lever = 1
			}
			// sign by side
			if strings.ToLower(pos.PosSide) == "short" {
				size = -size
			}
			positions[symbol] = okxPositionMeta{
				Size:       size,
				Leverage:   int(lever),
				MarginMode: strings.ToLower(pos.MarginMode),
			}
		}
	}
	return positions, nil
}
