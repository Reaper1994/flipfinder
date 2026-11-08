package main

import (
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
