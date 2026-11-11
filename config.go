package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Config is loaded from watchlist.json. Secrets come from the environment.
type Config struct {
	Marketplace string `json:"marketplace"` // e.g. EBAY_GB
	Postcode    string `json:"postcode"`    // outward code like "UB3"; improves delivery estimates

	// Auctions are only worth alerting on near the end, when the price is real.
	AuctionMaxHoursLeft float64 `json:"auction_max_hours_left"`
	AuctionMaxBids      int     `json:"auction_max_bids"`

	// Your selling costs as a fraction of the sale price (0 for UK private sellers
	// on eBay or local Facebook sales; set e.g. 0.13 if you sell as a business).
	SellFeePct float64 `json:"sell_fee_pct"`

	// Buy It Now listings below this fraction of market price are almost always
	// fakes, scams or mislisted parts (e.g. a "sealed 4TB SSD" for a third of
	// the going rate). Default 0.45; auctions are exempt as bids start low.
	SuspiciousBelow float64 `json:"suspicious_below"`

	GlobalExclude []string `json:"global_exclude"`
	Targets       []Target `json:"targets"`
}

type Target struct {
	Name string `json:"name"`

	// Normal searches and deliberate misspellings. Typo hits are flagged, as
	// fewer buyers see them.
	Queries     []string `json:"queries"`
	TypoQueries []string `json:"typo_queries"`

	// What it reliably resells for (check eBay sold listings / CeX). Update
	// this every few weeks — it drives every profit number.
	MarketPrice float64 `json:"market_price"`

	MaxBuyRatio float64 `json:"max_buy_ratio"` // only consider listings at or below this × market price
	MinProfit   float64 `json:"min_profit"`    // £ after costs
	MinPrice    float64 `json:"min_price"`     // below this it's almost always a box, cable or controller

	Exclude    []string `json:"exclude"`
	RequireAny []string `json:"require_any"` // title must contain at least one, e.g. "m2" for a Mac mini M2
	// Extra word groups for items that need several things at once: the title must
	// contain at least one word from every group, e.g. a tyre size, "all season" and "pair".
	RequireAll [][]string `json:"require_all"`

	// Only new items (eBay condition "New…"), for things like tyres whose
	// market_price is a new price and where used ones are worth far less.
	NewOnly bool `json:"new_only"`

	// Buying for yourself rather than to resell: alert on anything whose total
	// (with postage) is at or under this many pounds, ignoring max_buy_ratio and
	// min_profit. market_price is then the typical price, used for the saving.
	BuyUnder float64 `json:"buy_under"`
}

// SearchCap is the highest price worth fetching from eBay for this target.
func (t Target) SearchCap() float64 {
	if t.BuyUnder > 0 {
		return t.BuyUnder
	}
	return t.MarketPrice * t.MaxBuyRatio
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // catch typos in the watchlist
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Marketplace == "" {
		c.Marketplace = "EBAY_GB"
	}
	if c.AuctionMaxHoursLeft == 0 {
		c.AuctionMaxHoursLeft = 3
	}
	if c.SuspiciousBelow == 0 {
		c.SuspiciousBelow = 0.45
	}
	if c.AuctionMaxBids == 0 {
		c.AuctionMaxBids = 3
	}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Name == "" || t.MarketPrice <= 0 || len(t.Queries)+len(t.TypoQueries) == 0 {
			return Config{}, fmt.Errorf("target %d needs name, market_price and queries", i)
		}
		if t.MaxBuyRatio == 0 {
			t.MaxBuyRatio = 0.75
		}
	}
	return c, nil
}
