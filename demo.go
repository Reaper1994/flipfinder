package main

import (
	"context"
	"strings"
	"time"
)

// demoSearcher returns fictional listings so you can see the pipeline work
// without any API keys. Prices and sellers are made up.
type demoSearcher struct{}

func (demoSearcher) Search(_ context.Context, q string, _ float64) ([]Listing, error) {
	ql := strings.ToLower(q)
	soon := time.Now().Add(95 * time.Minute).UTC().Format(time.RFC3339)
	later := time.Now().Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	switch {
	case strings.Contains(ql, "playstaion"): // typo search finds the hidden bargain
		return []Listing{
			demoItem("d4", "Playstaion 5 console digital + 2 games", "", "190.00", 2, []string{"AUCTION"}, soon, "Used", "Uxbridge", 33, "98.9", "0.00"),
		}, nil
	case strings.Contains(ql, "mac mni"):
		return []Listing{
			demoItem("m1", "Apple mac mni M2 8GB 256GB silver - fully working", "295.00", "", 0, []string{"FIXED_PRICE", "BEST_OFFER"}, "", "Used", "Southall", 61, "100.0", "0.00"),
		}, nil
	case strings.Contains(ql, "ps5") || strings.Contains(ql, "playstation") || strings.Contains(ql, "playstaion"):
		return []Listing{
			demoItem("d1", "Sony PS5 Slim Disc Edition 1TB, boxed, 1 controller, barely used", "255.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Hayes", 212, "100.0", "5.99"),
			demoItem("d2", "PS5 empty box only", "15.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Slough", 40, "99.1", ""),
			demoItem("d3", "PS5 DualSense controller white", "38.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Ealing", 90, "100.0", "3.50"),
			demoItem("d5", "PS5 console - banned from PSN, works offline", "180.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Harrow", 5, "100.0", ""),
			demoItem("d6", "PS5 Disc console", "", "120.00", 1, []string{"AUCTION"}, later, "Used", "Reading", 150, "100.0", "8.00"),
		}, nil
	case strings.Contains(ql, "mac"):
		return []Listing{
			demoItem("m7", "Mac mini M2 8GB 256GB, boxed, AppleCare until 2027", "289.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Hayes", 7, "100.0", ""),
			demoItem("m2", "Mac mini M2 icloud locked spares", "120.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Croydon", 3, "100.0", ""),
			demoItem("m3", "Apple Mac mini M2 16GB 512GB, no power cable", "330.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Hounslow", 410, "99.8", "6.00"),
			demoItem("m4", "Mac mini 2014 i5 8GB", "95.00", "", 0, []string{"FIXED_PRICE"}, "", "Used", "Wembley", 75, "100.0", "7.00"),
		}, nil
	}
	return nil, nil
}

func demoItem(id, title, price, bid string, bids int, opts []string, end, cond, city string, fb int, fbPct, ship string) Listing {
	l := Listing{
		ItemID: id, Title: title, BidCount: bids, BuyingOptions: opts, ItemEndDate: end,
		Condition: cond, ItemWebURL: "https://www.ebay.co.uk/itm/demo-" + id,
	}
	if price != "" {
		l.Price = &Money{Value: price, Currency: "GBP"}
	}
	if bid != "" {
		l.CurrentBidPrice = &Money{Value: bid, Currency: "GBP"}
		l.Price = l.CurrentBidPrice
	}
	l.ItemLocation = &struct {
		City       string `json:"city"`
		PostalCode string `json:"postalCode"`
	}{City: city}
	l.Seller = &struct {
		Username           string `json:"username"`
		FeedbackPercentage string `json:"feedbackPercentage"`
		FeedbackScore      int    `json:"feedbackScore"`
	}{Username: "demo_seller", FeedbackPercentage: fbPct, FeedbackScore: fb}
	if ship != "" {
		l.ShippingOptions = append(l.ShippingOptions, struct {
			ShippingCost     *Money `json:"shippingCost"`
			ShippingCostType string `json:"shippingCostType"`
		}{ShippingCost: &Money{Value: ship, Currency: "GBP"}, ShippingCostType: "FIXED"})
	}
	return l
}

// demoChecker imitates Jev's typed answers with simple keyword rules.
type demoChecker struct{}

func (demoChecker) Check(_ context.Context, t Target, d Deal) (ListingAnswers, error) {
	title := strings.ToLower(d.Listing.Title)
	a := ListingAnswers{
		Matches:   jevNoul{0.95},
		Genuine:   jevNoul{0.9},
		Risk:      jevNoul{0.05},
		Condition: jevScore{Score: 2.2, Confidence: 0.8},
	}
	if strings.Contains(title, "banned") || strings.Contains(title, "locked") {
		a.Risk.Noul = 0.9
	}
	if strings.Contains(title, "2014") {
		a.Matches.Noul = 0.1
	}
	if strings.Contains(title, "no power cable") {
		a.Condition.Score = 1.4
	}
	if strings.Contains(title, "boxed") {
		a.Condition.Score = 2.8
	}
	if d.Listing.Seller != nil && d.Listing.Seller.FeedbackScore < 10 {
		a.Risk.Noul = max(a.Risk.Noul, 0.35)
	}
	return a, nil
}
