package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Deal is a listing that passed the price rules, plus what we worked out about it.
type Deal struct {
	Target  string
	Listing Listing

	Price    float64 // asking price or current bid
	Shipping float64
	Cost     float64 // price + shipping
	Resale   float64 // market price after selling fees
	Profit   float64
	ROI      float64 // profit / cost

	Auction bool
	EndsIn  time.Duration
	Typo    bool
	Flags   []string // human-readable warnings and highlights
	Verdict string   // set by the Jev check, if enabled
	Score   float64  // ranking: higher is better
	Caution bool     // something needs a human look before buying

	Rank, Total int // position in this run's alerts, e.g. 2 of 5
}

// Evaluate applies the cheap, deterministic rules. It returns ok=false with a
// reason when the listing isn't worth a closer look. Only survivors are sent
// to Jev, which keeps API usage (and cost) low.
func Evaluate(cfg Config, t Target, l Listing, typo bool, now time.Time) (Deal, bool, string) {
	title := strings.ToLower(l.Title)

	for _, words := range [2][]string{cfg.GlobalExclude, t.Exclude} {
		for _, ex := range words {
			if ex != "" && strings.Contains(title, strings.ToLower(ex)) {
				return Deal{}, false, "excluded word: " + ex
			}
		}
	}
	if len(t.RequireAny) > 0 && !slices.ContainsFunc(t.RequireAny, func(s string) bool {
		return strings.Contains(title, strings.ToLower(s))
	}) {
		return Deal{}, false, "missing required word"
	}

	d := Deal{Target: t.Name, Listing: l, Typo: typo}

	// Pure auctions use the current bid; Buy It Now uses the asking price.
	d.Auction = slices.Contains(l.BuyingOptions, "AUCTION") && !slices.Contains(l.BuyingOptions, "FIXED_PRICE")
	var ok bool
	if d.Auction && l.CurrentBidPrice != nil {
		d.Price, ok = l.CurrentBidPrice.Float()
	} else {
		d.Price, ok = l.Price.Float()
	}
	if !ok {
		return Deal{}, false, "no price"
	}

	if d.Auction {
		end, err := time.Parse(time.RFC3339, l.ItemEndDate)
		if err != nil {
			return Deal{}, false, "auction with no end date"
		}
		d.EndsIn = end.Sub(now)
		if d.EndsIn <= 0 || d.EndsIn.Hours() > cfg.AuctionMaxHoursLeft {
			return Deal{}, false, "auction not ending soon"
		}
		if l.BidCount > cfg.AuctionMaxBids {
			return Deal{}, false, "auction has too many bids"
		}
		d.Flags = append(d.Flags, fmt.Sprintf("⏱ auction ends in %s, %d bid(s) — final price will be higher", shortDur(d.EndsIn), l.BidCount))
	}

	d.Shipping, ok = cheapestShipping(l)
	if !ok {
		d.Flags = append(d.Flags, "📍 collection or calculated postage — check")
		d.Caution = true
	}
	d.Cost = d.Price + d.Shipping

	if d.Cost < t.MinPrice {
		return Deal{}, false, "too cheap: probably an accessory, box or wanted ad"
	}
	if !d.Auction && d.Cost < t.MarketPrice*cfg.SuspiciousBelow {
		return Deal{}, false, "suspiciously cheap: likely fake, scam or mislisted"
	}
	if d.Cost > t.MarketPrice*t.MaxBuyRatio {
		return Deal{}, false, "not cheap enough"
	}

	d.Resale = t.MarketPrice * (1 - cfg.SellFeePct)
	d.Profit = d.Resale - d.Cost
	if d.Profit < t.MinProfit {
		return Deal{}, false, "profit below minimum"
	}
	d.ROI = d.Profit / d.Cost

	if typo {
		d.Flags = append(d.Flags, "🔤 misspelled title — fewer buyers will find it")
	}
	if s := l.Seller; s != nil {
		pct, _ := strconv.ParseFloat(s.FeedbackPercentage, 64)
		if s.FeedbackScore < 10 || (pct > 0 && pct < 97) {
			d.Flags = append(d.Flags, fmt.Sprintf("⚠️ low-feedback seller (%d, %.1f%%) — pay through eBay only", s.FeedbackScore, pct))
			d.Caution = true
		}
	}

	// Rank by profit, nudged up for good return on money and misspellings.
	d.Score = d.Profit * (1 + math.Min(d.ROI, 1))
	if typo {
		d.Score *= 1.15
	}
	return d, true, ""
}

func cheapestShipping(l Listing) (float64, bool) {
	best, found := 0.0, false
	for _, o := range l.ShippingOptions {
		if v, ok := o.ShippingCost.Float(); ok && (!found || v < best) {
			best, found = v, true
		}
	}
	return best, found
}

func shortDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
