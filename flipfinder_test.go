package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testCfg = Config{
	AuctionMaxHoursLeft: 3, AuctionMaxBids: 3,
	GlobalExclude: []string{"box only", "icloud locked"},
}
var ps5 = Target{Name: "PS5", MarketPrice: 320, MaxBuyRatio: 0.8, MinProfit: 40, MinPrice: 140}

func listing(title, price string, opts ...string) Listing {
	if len(opts) == 0 {
		opts = []string{"FIXED_PRICE"}
	}
	return Listing{ItemID: title, Title: title, Price: &Money{Value: price}, BuyingOptions: opts}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		l    Listing
		ok   bool
	}{
		{"good deal", listing("PS5 slim console", "230.00"), true},
		{"excluded word", listing("PS5 BOX ONLY", "20.00"), false},
		{"accessory price", listing("PS5 console controller", "35.00"), false},
		{"not cheap enough", listing("PS5 console", "300.00"), false},
	}
	for _, c := range cases {
		_, ok, why := Evaluate(testCfg, ps5, c.l, false, now)
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v (%s)", c.name, ok, c.ok, why)
		}
	}

	strict := ps5
	strict.MinProfit = 100
	if _, ok, _ := Evaluate(testCfg, strict, listing("PS5 console", "230.00"), false, now); ok {
		t.Error("£90 profit should fail a £100 minimum")
	}

	// Auctions: use the current bid, only near the end, only with few bids.
	a := listing("PS5 console", "100.00", "AUCTION")
	a.CurrentBidPrice = &Money{Value: "180.00"}
	a.BidCount = 2
	a.ItemEndDate = now.Add(time.Hour).Format(time.RFC3339)
	d, ok, why := Evaluate(testCfg, ps5, a, false, now)
	if !ok || d.Price != 180 {
		t.Fatalf("ending auction: ok=%v price=%v (%s)", ok, d.Price, why)
	}
	a.ItemEndDate = now.Add(48 * time.Hour).Format(time.RFC3339)
	if _, ok, _ := Evaluate(testCfg, ps5, a, false, now); ok {
		t.Error("auction ending in 2 days should be skipped")
	}
	a.ItemEndDate = now.Add(time.Hour).Format(time.RFC3339)
	a.BidCount = 9
	if _, ok, _ := Evaluate(testCfg, ps5, a, false, now); ok {
		t.Error("auction with many bids should be skipped")
	}
}

func TestShippingCountsTowardCost(t *testing.T) {
	l := listing("PS5 console", "240.00")
	l.ShippingOptions = append(l.ShippingOptions, struct {
		ShippingCost     *Money `json:"shippingCost"`
		ShippingCostType string `json:"shippingCostType"`
	}{ShippingCost: &Money{Value: "20.00"}})
	if _, ok, _ := Evaluate(testCfg, ps5, l, false, time.Now()); ok {
		t.Error("£240 + £20 postage is over the £256 buy limit")
	}
}

func TestDecide(t *testing.T) {
	good := ListingAnswers{Matches: jevNoul{0.9}, Genuine: jevNoul{0.9}, Risk: jevNoul{0.05}, Condition: jevScore{Score: 2.2}}
	if keep, _, _ := Decide(good); !keep {
		t.Error("good listing rejected")
	}
	for name, mod := range map[string]func(*ListingAnswers){
		"risky":      func(a *ListingAnswers) { a.Risk.Noul = 0.7 },
		"wrong item": func(a *ListingAnswers) { a.Matches.Noul = 0.2 },
		"box only":   func(a *ListingAnswers) { a.Genuine.Noul = 0.1 },
		"faulty":     func(a *ListingAnswers) { a.Condition.Score = 0.4 },
	} {
		a := good
		mod(&a)
		if keep, _, _ := Decide(a); keep {
			t.Errorf("%s: should be rejected", name)
		}
	}
	worn := good
	worn.Condition.Score = 1.3
	if keep, _, mult := Decide(worn); !keep || mult >= 1 {
		t.Errorf("worn item: keep=%v mult=%v, want kept with lower score", keep, mult)
	}
}

func TestStoreDedupesAndRealertsOnPriceDrop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")
	now := time.Now()
	s, _ := OpenStore(path)
	s.Mark("x", 200, now)
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}
	s, _ = OpenStore(path)
	if s.ShouldAlert("x", 199) {
		t.Error("tiny price change should not re-alert")
	}
	if !s.ShouldAlert("x", 185) {
		t.Error("7.5% price drop should re-alert")
	}
}

type countingNotifier struct{ n int }

func (c *countingNotifier) Send(context.Context, Deal) error { c.n++; return nil }

func TestDemoRunAlertsOnceOnly(t *testing.T) {
	cfg, err := LoadConfig("testdata/demo-watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	n := &countingNotifier{}
	r := &Runner{Cfg: cfg, Search: demoSearcher{}, Check: demoChecker{}, Notify: n,
		StatePath: filepath.Join(t.TempDir(), "seen.json"), MaxSend: 10}
	if err := r.RunOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n.n != 3 {
		t.Fatalf("first run sent %d alerts, want 3", n.n)
	}
	if err := r.RunOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n.n != 3 {
		t.Fatalf("second run re-sent alerts: total %d", n.n)
	}
}

func TestDryRunDoesNotUseUpAlerts(t *testing.T) {
	cfg, err := LoadConfig("testdata/demo-watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "seen.json")
	dry := &countingNotifier{}
	r := &Runner{Cfg: cfg, Search: demoSearcher{}, Check: demoChecker{}, Notify: dry,
		StatePath: state, ReadOnly: true, MaxSend: 10}
	if err := r.RunOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	live := &countingNotifier{}
	r.Notify, r.ReadOnly = live, false
	if err := r.RunOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if dry.n != 3 || live.n != 3 {
		t.Fatalf("dry run printed %d, live run sent %d; want 3 and 3", dry.n, live.n)
	}
}

func TestAeronFilterKeepsLoadedChairsDropsParts(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	var aeron Target
	for _, tg := range cfg.Targets {
		if strings.Contains(tg.Name, "Aeron") {
			aeron = tg
		}
	}
	now := time.Now()
	keep := listing("Herman Miller Aeron Size B Fully Loaded Gaming Chair with Brand New Headrest, PostureFit and Gaslift, adjustable arms", "250")
	if _, ok, why := Evaluate(cfg, aeron, keep, false, now); !ok {
		t.Errorf("loaded Aeron skipped: %s", why)
	}
	for _, title := range []string{
		"Headrest for Herman Miller Aeron chair",
		"Herman Miller Aeron arm pads replacement pair",
		"Herman Miller Aeron gas lift only",
	} {
		if _, ok, _ := Evaluate(cfg, aeron, listing(title, "250"), false, now); ok {
			t.Errorf("part listing kept: %q", title)
		}
	}
}

func TestChairTargetsKeepChairsDropParts(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	find := func(sub string) Target {
		for _, tg := range cfg.Targets {
			if strings.Contains(tg.Name, sub) {
				return tg
			}
		}
		t.Fatalf("no target containing %q", sub)
		return Target{}
	}
	now := time.Now()
	cases := []struct {
		target, title, price string
		keep                 bool
	}{
		{"Mirra", "Herman Miller Mirra 2 office chair fully adjustable graphite", "150", true},
		{"Mirra", "Herman Miller Mirra 2 arms only pair", "60", false},
		{"Hbada", "HBADA P3 ergonomic office chair with footrest, barely used", "40", true},
		{"Hbada", "Sihoo Doro C300 ergonomic desk chair black", "45", true},
		{"Hbada", "Hbada chair footrest only", "30", false},
	}
	for _, c := range cases {
		_, ok, why := Evaluate(cfg, find(c.target), listing(c.title, c.price), false, now)
		if ok != c.keep {
			t.Errorf("%q: kept=%v want %v (%s)", c.title, ok, c.keep, why)
		}
	}
}
