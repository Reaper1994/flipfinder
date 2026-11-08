package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkEvaluate(b *testing.B) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		b.Fatal(err)
	}
	t := findTarget(b, cfg, "PS5")
	now := time.Now()
	l := listing("Sony PlayStation 5 Slim Disc Edition 1TB console, boxed with controller", "220")
	b.ReportAllocs()
	for b.Loop() {
		Evaluate(cfg, t, l, false, now)
	}
}

func BenchmarkJevPayload(b *testing.B) {
	j := &Jev{Model: "jev-latest"}
	t := Target{Name: "PS5 console", MarketPrice: 320}
	d := Deal{Price: 210, Listing: listing("Sony PlayStation 5 Disc Edition console, boxed with controller", "210")}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := j.payload(t, d); err != nil {
			b.Fatal(err)
		}
	}
}

// slowSearcher imitates network latency so the benchmark shows what bounded
// concurrency buys over one-at-a-time searching.
type slowSearcher struct{ delay time.Duration }

func (s slowSearcher) Search(ctx context.Context, q string, _ float64) ([]Listing, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []Listing{listing(q+" console", "220")}, nil
}

func BenchmarkRunOnceSearch(b *testing.B) {
	cfg, err := LoadConfig("watchlist.json")
	if err != nil {
		b.Fatal(err)
	}
	for _, workers := range []int{1, defaultSearchWorkers} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			r := &Runner{Cfg: cfg, Search: slowSearcher{5 * time.Millisecond}, Notify: &countingNotifier{},
				ReadOnly: true, MaxSend: 8, SearchWorkers: workers}
			for b.Loop() {
				if err := r.RunOnce(context.Background(), time.Now()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type slowChecker struct{ delay time.Duration }

func (c slowChecker) Check(ctx context.Context, _ Target, _ Deal) (ListingAnswers, error) {
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return ListingAnswers{}, ctx.Err()
	}
	return ListingAnswers{Matches: jevNoul{1}, Genuine: jevNoul{1}, Condition: jevScore{Score: 2}}, nil
}

func BenchmarkRunOnceCheck(b *testing.B) {
	cfg := testCfg
	cfg.Targets = []Target{{Name: "PS5", Queries: []string{"ps5"}, MarketPrice: 320, MaxBuyRatio: 0.8, MinProfit: 40, MinPrice: 140}}
	for _, workers := range []int{1, defaultCheckWorkers} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			r := &Runner{Cfg: cfg, Search: tiedSearcher{}, Check: slowChecker{20 * time.Millisecond},
				Notify: &countingNotifier{}, ReadOnly: true, MaxSend: 8, CheckWorkers: workers}
			for b.Loop() {
				if err := r.RunOnce(context.Background(), time.Now()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
