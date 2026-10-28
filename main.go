// flipfinder watches eBay UK for underpriced small electronics (Mac minis,
// consoles, etc.), checks each candidate with Jev, and sends the best ones to
// Telegram. It uses eBay's official Browse API — no scraping.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"
)

func main() {
	var (
		cfgPath = flag.String("config", "watchlist.json", "watchlist file")
		state   = flag.String("state", "seen.json", "file that remembers alerted listings")
		demo    = flag.Bool("demo", false, "use built-in sample listings; needs no keys and sends nothing")
		dry     = flag.Bool("dry", false, "print deals instead of sending to Telegram")
		every   = flag.Duration("every", 0, "keep running and check this often, e.g. 15m (default: run once, for cron/GitHub Actions)")
		maxSend = flag.Int("max", 8, "maximum alerts per run")
		verbose = flag.Bool("v", false, "log why listings were skipped")
		ping    = flag.Bool("ping", false, "send one test message to Telegram and exit")
	)
	flag.Parse()
	log.SetFlags(log.Ltime)

	if *ping {
		tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
		if tok == "" || chat == "" {
			log.Fatal("set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		msg := "✅ <b>flipfinder is connected</b>\nAlerts for underpriced eBay listings will arrive in this chat."
		if err := NewTelegram(tok, chat).SendText(ctx, msg); err != nil {
			log.Fatal(err)
		}
		log.Print("test message sent to Telegram")
		return
	}

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	var (
		search  Searcher
		checker Checker
		notify  Notifier = Console{}
	)
	if *demo {
		search, checker = demoSearcher{}, demoChecker{}
		*state = "" // in-memory only
	} else {
		// Trim stray spaces/newlines that often come along when keys are pasted.
		id, secret := strings.TrimSpace(os.Getenv("EBAY_CLIENT_ID")), strings.TrimSpace(os.Getenv("EBAY_CLIENT_SECRET"))
		if id == "" || secret == "" {
			log.Fatal("set EBAY_CLIENT_ID and EBAY_CLIENT_SECRET (or try -demo)")
		}
		base, err := EbayBaseURL(os.Getenv("EBAY_ENV"))
		if err != nil {
			log.Fatal(err)
		}
		if strings.Contains(id, "-SBX-") && !strings.Contains(base, "sandbox") {
			log.Print("EBAY_CLIENT_ID looks like a sandbox key; set EBAY_ENV=sandbox")
		}
		log.Printf("eBay keys: App ID %s, Cert ID %s, host %s", keyKind(id, "-"), keyKind(secret, ""), base)
		search = NewEbayClient(id, secret, base, cfg.Marketplace, cfg.Postcode)

		if key := os.Getenv("TYPESAFE_API_KEY"); key != "" {
			j, err := NewJev(envOr("TYPESAFE_URL", "https://api.typesafe.ai"), key, envOr("FLIP_JEV_MODEL", "jev-latest"))
			if err != nil {
				log.Fatal(err)
			}
			checker = j
		} else {
			log.Print("TYPESAFE_API_KEY not set: price rules only, no Jev check")
		}

		if !*dry {
			tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
			if tok == "" || chat == "" {
				log.Fatal("set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID (or use -dry)")
			}
			notify = NewTelegram(tok, chat)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := &Runner{Cfg: cfg, Search: search, Check: checker, Notify: notify,
		StatePath: *state, MaxSend: *maxSend, Verbose: *verbose}

	for {
		if err := r.RunOnce(ctx, time.Now()); err != nil {
			log.Print(err)
			if *every == 0 {
				os.Exit(1)
			}
		}
		if *every == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*every):
		}
	}
}

type Runner struct {
	Cfg       Config
	Search    Searcher
	Check     Checker // optional
	Notify    Notifier
	StatePath string
	MaxSend   int
	Verbose   bool
}

func (r *Runner) RunOnce(ctx context.Context, now time.Time) error {
	store, err := OpenStore(r.StatePath)
	if err != nil {
		return err
	}

	byID := map[string]Deal{}
	var searchErrs []string
	for _, t := range r.Cfg.Targets {
		maxPrice := t.MarketPrice * t.MaxBuyRatio
		// A listing counts as a "typo find" only if normal searches missed it.
		found := map[string]bool{}
		run := func(q string, typo bool) {
			listings, err := r.Search.Search(ctx, q, maxPrice)
			if err != nil {
				searchErrs = append(searchErrs, err.Error())
				return
			}
			for _, l := range listings {
				if found[l.ItemID] { // already seen by an earlier query for this target
					continue
				}
				found[l.ItemID] = true
				d, ok, why := Evaluate(r.Cfg, t, l, typo, now)
				if !ok {
					if r.Verbose {
						log.Printf("skip %-45.45q %s", l.Title, why)
					}
					continue
				}
				if prev, dup := byID[l.ItemID]; !dup || d.Score > prev.Score {
					byID[l.ItemID] = d
				}
			}
		}
		for _, q := range t.Queries {
			run(q, false)
		}
		for _, q := range t.TypoQueries {
			run(q, true)
		}
	}

	// Drop what we've already alerted on before spending Jev calls.
	var deals []Deal
	for _, d := range byID {
		if store.ShouldAlert(d.Listing.ItemID, d.Cost) {
			deals = append(deals, d)
		}
	}

	if r.Check != nil {
		targets := map[string]Target{}
		for _, t := range r.Cfg.Targets {
			targets[t.Name] = t
		}
		kept := deals[:0]
		for _, d := range deals {
			a, err := r.Check.Check(ctx, targets[d.Target], d)
			if err != nil {
				d.Verdict = "⚪ Jev check failed — review it yourself"
				kept = append(kept, d)
				continue
			}
			keep, verdict, mult := Decide(a)
			if !keep {
				if r.Verbose {
					log.Printf("jev rejected %-40.40q %s", d.Listing.Title, verdict)
				}
				store.Mark(d.Listing.ItemID, d.Cost, now) // don't re-check it every run
				continue
			}
			d.Verdict, d.Score = verdict, d.Score*mult
			kept = append(kept, d)
		}
		deals = kept
	}

	sort.Slice(deals, func(i, j int) bool { return deals[i].Score > deals[j].Score })
	if len(deals) > r.MaxSend {
		deals = deals[:r.MaxSend]
	}
	sent := 0
	for _, d := range deals {
		if err := r.Notify.Send(ctx, d); err != nil {
			log.Print(err)
			continue
		}
		store.Mark(d.Listing.ItemID, d.Cost, now)
		sent++
	}
	log.Printf("checked %d targets, %d candidate(s), %d alert(s) sent", len(r.Cfg.Targets), len(byID), sent)

	if err := store.Save(now); err != nil {
		return err
	}
	if len(searchErrs) > 0 {
		return fmt.Errorf("some searches failed:\n  %s", strings.Join(searchErrs, "\n  "))
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// keyKind reports whether an eBay key is a production or sandbox one without
// revealing it: App IDs contain "-PRD-"/"-SBX-", Cert IDs start with "PRD-"/"SBX-".
func keyKind(key, sep string) string {
	switch {
	case strings.Contains(key, sep+"PRD-") && (sep != "" || strings.HasPrefix(key, "PRD-")):
		return fmt.Sprintf("production (%d chars)", len(key))
	case strings.Contains(key, sep+"SBX-") && (sep != "" || strings.HasPrefix(key, "SBX-")):
		return fmt.Sprintf("SANDBOX (%d chars)", len(key))
	}
	return fmt.Sprintf("UNRECOGNISED (%d chars; not an App ID or Cert ID?)", len(key))
}
