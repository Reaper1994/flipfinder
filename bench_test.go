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
