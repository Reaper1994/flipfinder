package main

import (
	"context"
	"encoding/json"
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

func (c *countingNotifier) SendIndex(context.Context, []Deal, int) error { return nil }

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

func TestPS5TargetKeepsConsolesDropsExtras(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	var ps5 Target
	for _, tg := range cfg.Targets {
		if tg.Name == "PS5 console" {
			ps5 = tg
		}
	}
	if ps5.Name == "" {
		t.Fatal("PS5 target missing")
	}
	now := time.Now()
	cases := []struct {
		title, price string
		keep         bool
	}{
		{"Sony PlayStation 5 Slim Disc Edition 1TB console, boxed", "220", true},
		{"Playstaion 5 digital edition console good condition", "200", true},
		{"PS5 Pro console 2TB boxed", "250", false},
		{"PS5 DualSense controller white", "160", false},
		{"PlayStation Portal remote player for PS5", "160", false},
	}
	for _, c := range cases {
		_, ok, why := Evaluate(cfg, ps5, listing(c.title, c.price), false, now)
		if ok != c.keep {
			t.Errorf("%q: kept=%v want %v (%s)", c.title, ok, c.keep, why)
		}
	}
}

func formatFixture() Deal {
	return Deal{Target: "PS5 <console>", Rank: 2, Total: 3, Price: 210, Cost: 210, Resale: 290, Profit: 80, ROI: 0.38, Score: 110,
		Verdict: "✅ looks genuine",
		Listing: Listing{Title: "PS5 & pad", ItemWebURL: "https://ebay.test/1?a=1&b=2"}}
}

func TestAdvice(t *testing.T) {
	d := formatFixture()
	if _, got := advice(d); got != "Buy" {
		t.Errorf("clean deal: got %q", got)
	}
	d.Auction = true
	if _, got := advice(d); got != "Bid" {
		t.Errorf("auction: got %q", got)
	}
	d.Caution = true
	if _, got := advice(d); got != "Check first" {
		t.Errorf("caution: got %q", got)
	}
	d = formatFixture()
	d.Verdict = ""
	if _, got := advice(d); got != "Check first" {
		t.Errorf("unchecked by Jev: got %q", got)
	}
}

func TestFormatDealHTML(t *testing.T) {
	got := formatDeal(formatFixture(), true)
	for _, want := range []string{
		"<b>#2/3 · 🟢 Buy · PS5 &lt;console&gt;</b>", "PS5 &amp; pad", "<pre>Price     £210.00\n",
		"Postage   check", "Score     110", "Advice    Buy</pre>", `href="https://ebay.test/1?a=1&amp;b=2"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatIndex(t *testing.T) {
	got := formatIndex([]Deal{formatFixture()}, 2, true)
	for _, want := range []string{"1 new deal</b>", "2. 🟢 Buy · <a href=", "~£80 profit · score 110", "+2 more next run"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func findTarget(t testing.TB, cfg Config, prefix string) Target {
	t.Helper()
	for _, tg := range cfg.Targets {
		if strings.HasPrefix(tg.Name, prefix) {
			return tg
		}
	}
	t.Fatalf("no target %q", prefix)
	return Target{}
}

func TestThuleBikeCarrierFilters(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := []struct {
		target, title, price string
		keep                 bool
	}{
		{"Thule towbar", "Thule EasyFold XT 2 towbar bike carrier, all parts included", "250", true},
		{"Thule towbar", "Thule VeloCompact 927 3 bike tow bar rack", "220", true},
		{"Thule towbar", "Thule Motion XT L roof box", "250", false},
		{"Thule towbar", "Thule VeloSpace XT bike extension", "150", false},
		{"Thule towbar", "Thule Chariot Cross 2 pushchair", "250", false},
		{"Thule roof", "Thule ProRide 598 roof mounted bike carrier", "70", true},
		{"Thule roof", "Thule ProRide 598 spare parts wheel holder", "50", false},
		{"Thule boot", "Thule OutWay 2 bike boot mounted carrier", "100", true},
	}
	for _, c := range cases {
		tg := findTarget(t, cfg, c.target)
		_, ok, why := Evaluate(cfg, tg, listing(c.title, c.price), false, now)
		if ok != c.keep {
			t.Errorf("%s / %q: kept=%v (%s), want %v", c.target, c.title, ok, why, c.keep)
		}
	}
}

func TestSuspiciouslyCheapBuyItNowIsSkipped(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	ssd := findTarget(t, cfg, "NVMe SSD 4TB")
	_, ok, why := Evaluate(cfg, ssd, listing("WD_BLACK SN850X 4TB NVMe SSD new sealed", "138"), false, time.Now())
	if ok || !strings.HasPrefix(why, "suspiciously cheap") {
		t.Errorf("£138 4TB SSD: kept=%v (%s), want suspiciously cheap", ok, why)
	}
}

func TestJevPayload(t *testing.T) {
	j := &Jev{Model: "m"}
	b, err := j.payload(ps5, Deal{Price: 210, Listing: listing("PS5", "210")})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		State     map[string]any            `json:"state"`
		Model     string                    `json:"model"`
		Questions map[string]map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "m" || got.State["target_product"] != "PS5" || len(got.Questions) != len(jevQuestions) {
		t.Errorf("unexpected payload: %s", b)
	}
}

// Results must not depend on how many searches run at once.
func TestRunOnceSameResultAnyConcurrency(t *testing.T) {
	cfg, err := LoadConfig("testdata/demo-watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, workers := range []int{1, 8} {
		n := &recordingNotifier{}
		r := &Runner{Cfg: cfg, Search: demoSearcher{}, Check: demoChecker{}, Notify: n,
			ReadOnly: true, MaxSend: 10, SearchWorkers: workers, CheckWorkers: workers}
		if err := r.RunOnce(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = n.ids
		} else if strings.Join(n.ids, ",") != strings.Join(want, ",") {
			t.Errorf("workers=%d sent %v, want %v", workers, n.ids, want)
		}
	}
	if len(want) == 0 {
		t.Fatal("no alerts sent")
	}
}

type recordingNotifier struct{ ids []string }

func (c *recordingNotifier) Send(_ context.Context, d Deal) error {
	c.ids = append(c.ids, d.Listing.ItemID)
	return nil
}

func (c *recordingNotifier) SendIndex(context.Context, []Deal, int) error { return nil }

// Equal-score deals must be ordered the same way on every run.
func TestRunOnceTieOrderIsStable(t *testing.T) {
	cfg := testCfg
	cfg.Targets = []Target{{Name: "PS5", Queries: []string{"ps5"}, MarketPrice: 320, MaxBuyRatio: 0.8, MinProfit: 40, MinPrice: 140}}
	var want string
	for i := 0; i < 20; i++ {
		n := &recordingNotifier{}
		r := &Runner{Cfg: cfg, Search: tiedSearcher{}, Notify: n, ReadOnly: true, MaxSend: 10}
		if err := r.RunOnce(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(n.ids, ",")
		if i == 0 {
			want = got
		} else if got != want {
			t.Fatalf("run %d order %s, first run %s", i, got, want)
		}
	}
	if want != "a,b,c,d,e,f" {
		t.Errorf("order = %s, want ties sorted by item ID", want)
	}
}

type tiedSearcher struct{}

func (tiedSearcher) Search(context.Context, string, SearchOptions) ([]Listing, error) {
	var out []Listing
	for _, id := range []string{"f", "e", "d", "c", "b", "a"} {
		l := listing("PS5 console", "230.00")
		l.ItemID = id
		out = append(out, l)
	}
	return out, nil
}

func TestTyreTargets(t *testing.T) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Target{}
	for _, tg := range cfg.Targets {
		byName[tg.Name] = tg
	}
	single := byName["Premium 235/45 R20 all-season tyre (single, new, any price)"]
	pair := byName["Premium 235/45 R20 all-season tyres (pair, new, any price)"]
	set := byName["Premium 235/45 R20 all-season tyres (set of 4, new, any price)"]
	if single.Name == "" || pair.Name == "" || set.Name == "" {
		t.Fatal("tyre targets missing from watchlist.json")
	}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		tg    Target
		title string
		price string
		ok    bool
	}{
		{"new single", single, "Michelin CrossClimate 2 235/45 R20 100V XL SUV 4x4", "130.00", true},
		{"single, other size", single, "Michelin CrossClimate 2 235/45 R19 99V", "130.00", false},
		{"single, summer tyre", single, "Pirelli P Zero 235/45R20 summer tyre", "130.00", false},
		{"single, part worn", single, "Pirelli 235/45 R20 all season part worn 6mm", "60.00", false},
		{"single, on alloys", single, "20 inch alloy wheels Pirelli 235/45 R20 all season", "130.00", false},
		{"single target skips a pair", single, "2x 235/45 R20 all season Goodyear Vector 4Seasons", "130.00", false},
		{"budget brand is not premium", single, "235/45 R20 100W Gripmax SureGrip A/S XL", "113.00", false},
		{"1x is a single, not a pair", single, "1x 235/45R20 100W Continental AllSeasonContact 2", "130.00", true},
		{"Pirelli all season", single, "Pirelli Scorpion All Season SF3 235/45 R20 100W", "150.00", true},
		{"pair", pair, "Pair 235/45 R20 Goodyear Vector 4Seasons all season", "260.00", true},
		{"pair target needs a pair", pair, "Goodyear 235/45 R20 all season tyre", "260.00", false},
		{"set of 4", set, "Set of 4 Bridgestone Turanza All Season 6 235/45 R20 unused", "500.00", true},
		{"set target ignores 4x4", set, "Pirelli 235/45 R20 all season tyre 4x4 SUV", "500.00", false},
	}
	for _, c := range cases {
		l := posted(listing(c.title, c.price))
		l.Condition = "New"
		_, ok, why := Evaluate(cfg, c.tg, l, false, now)
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v (%s)", c.name, ok, c.ok, why)
		}
	}

	// Any price: dear tyres still alert, showing how they compare with typical.
	dear := posted(listing("Michelin CrossClimate 3 235/45 R20 100V XL", "230.00"))
	dear.Condition = "New"
	d, ok, why := Evaluate(cfg, single, dear, false, now)
	if !ok || !d.ForMe {
		t.Fatalf("£230 tyre with no limit: ok=%v (%s)", ok, why)
	}
	msg := formatDeal(d, false)
	if strings.Contains(msg, "Limit") || strings.Contains(msg, "Resale") || !strings.Contains(msg, "above typical") {
		t.Errorf("any-price alert text:\n%s", msg)
	}

	used := posted(listing("Michelin CrossClimate 2 235/45 R20 100V XL", "120.00"))
	used.Condition = "Used"
	if _, ok, _ := Evaluate(cfg, single, used, false, now); ok {
		t.Error("a used tyre must not match a new-only target")
	}

	// Collection-only listings count from anywhere in the UK, flagged to check.
	collect := listing("Set of 4 Pirelli Scorpion All Season SF3 235/45 R20", "550.00")
	collect.Condition = "New" // listing() sets no postage options: collection only
	d, ok, why = Evaluate(cfg, set, collect, false, now)
	if !ok || !strings.Contains(formatDeal(d, false), "collection or calculated postage") {
		t.Errorf("collection-only set: ok=%v (%s)", ok, why)
	}
}

// posted gives a test listing free postage, so it isn't collection-only.
func posted(l Listing) Listing {
	l.ShippingOptions = append(l.ShippingOptions, struct {
		ShippingCost     *Money `json:"shippingCost"`
		ShippingCostType string `json:"shippingCostType"`
	}{ShippingCost: &Money{Value: "0.00"}})
	return l
}
