// flipfinder watches eBay UK for underpriced small electronics (Mac minis,
// consoles, etc.), checks each candidate with Jev, and sends the best ones to
// Telegram. It uses eBay's official Browse API — no scraping.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
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
		if err := runPing(); err != nil {
			log.Fatal(err)
		}
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
		StatePath: *state, ReadOnly: *dry, MaxSend: *maxSend, Verbose: *verbose}

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
	ReadOnly  bool // dry runs read seen.json but never update it
	MaxSend   int
	Verbose   bool

	// SearchWorkers and CheckWorkers bound concurrent eBay searches and Jev
	// checks. Zero means the default.
	SearchWorkers, CheckWorkers int
}

const (
	defaultSearchWorkers = 4
	defaultCheckWorkers  = 4
)

func (r *Runner) searchWorkers() int {
	if r.SearchWorkers > 0 {
		return r.SearchWorkers
	}
	return defaultSearchWorkers
}

func (r *Runner) checkWorkers() int {
	if r.CheckWorkers > 0 {
		return r.CheckWorkers
	}
	return defaultCheckWorkers
}

func (r *Runner) RunOnce(ctx context.Context, now time.Time) error {
	store, err := OpenStore(r.StatePath)
	if err != nil {
		return err
	}

	// Run every query concurrently (bounded, to stay inside eBay's rate limits),
	// then merge the results in watchlist order so output is deterministic.
	type job struct {
		ti       int // index into Cfg.Targets
		t        Target
		query    string
		typo     bool
		pickup   bool // eBay local-pickup search within t.PickupRadiusMiles
		listings []Listing
		err      error
	}
	var jobs []job
	for ti, t := range r.Cfg.Targets {
		for _, q := range t.Queries {
			jobs = append(jobs, job{ti: ti, t: t, query: q})
		}
		for _, q := range t.TypoQueries {
			jobs = append(jobs, job{ti: ti, t: t, query: q, typo: true})
		}
		if t.PickupRadiusMiles > 0 && r.Cfg.Postcode != "" {
			for _, q := range t.Queries {
				jobs = append(jobs, job{ti: ti, t: t, query: q, pickup: true})
			}
		}
	}
	forEach(len(jobs), r.searchWorkers(), func(i int) {
		j := &jobs[i]
		opt := SearchOptions{MaxPrice: j.t.SearchCap(), NewOnly: j.t.NewOnly}
		if j.pickup {
			opt.PickupRadiusMiles, opt.PickupPostcode = j.t.PickupRadiusMiles, r.Cfg.Postcode
		}
		j.listings, j.err = r.Search.Search(ctx, j.query, opt)
	})

	// Listings the local-pickup search returned can be collected within range.
	near := map[int]map[string]bool{}
	for _, j := range jobs {
		if !j.pickup || j.err != nil {
			continue
		}
		if near[j.ti] == nil {
			near[j.ti] = map[string]bool{}
		}
		for _, l := range j.listings {
			near[j.ti][l.ItemID] = true
		}
	}

	byID := map[string]Deal{}
	var searchErrs []string
	// A listing counts as a "typo find" only if normal searches missed it.
	found := map[int]map[string]bool{} // target index -> item IDs already seen
	for _, j := range jobs {
		if j.err != nil {
			searchErrs = append(searchErrs, j.err.Error())
			continue
		}
		if found[j.ti] == nil {
			found[j.ti] = map[string]bool{}
		}
		seen := found[j.ti]
		for _, l := range j.listings {
			if seen[l.ItemID] { // already seen by an earlier query for this target
				continue
			}
			seen[l.ItemID] = true
			l.NearPickup = near[j.ti][l.ItemID]
			d, ok, why := Evaluate(r.Cfg, j.t, l, j.typo, now)
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
		// Checks are independent network calls: run them concurrently, then
		// apply the verdicts in order (Store is not safe for concurrent use).
		answers := make([]ListingAnswers, len(deals))
		errs := make([]error, len(deals))
		forEach(len(deals), r.checkWorkers(), func(i int) {
			answers[i], errs[i] = r.Check.Check(ctx, targets[deals[i].Target], deals[i])
		})
		kept := deals[:0]
		for i, d := range deals {
			a, err := answers[i], errs[i]
			if err != nil {
				d.Verdict = "⚪ Jev check failed — review it yourself"
				d.Caution = true
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
			d.Caution = d.Caution || mult < 1
			kept = append(kept, d)
		}
		deals = kept
	}

	// Ties are broken by item ID: deals come from a map, so without a total
	// order equal-score deals could swap places (and which ones are held back
	// by MaxSend) from run to run.
	slices.SortFunc(deals, func(a, b Deal) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(a.Listing.ItemID, b.Listing.ItemID)
	})
	held := 0
	if len(deals) > r.MaxSend {
		held = len(deals) - r.MaxSend
		deals = deals[:r.MaxSend]
	}
	for i := range deals {
		deals[i].Rank, deals[i].Total = i+1, len(deals)
	}
	if len(deals) > 0 {
		if err := r.Notify.SendIndex(ctx, deals, held); err != nil {
			log.Print(err)
		}
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

	if !r.ReadOnly {
		if err := store.Save(now); err != nil {
			return err
		}
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

// runPing checks each configured service without touching eBay: it sends
// Telegram a test message and asks Jev about one fictional listing.
func runPing() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tested := 0

	if tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"); tok != "" && chat != "" {
		msg := "✅ <b>flipfinder is connected</b>\nAlerts for underpriced eBay listings will arrive in this chat."
		if err := NewTelegram(tok, chat).SendText(ctx, msg); err != nil {
			return fmt.Errorf("telegram test failed: %w", err)
		}
		log.Print("telegram: test message sent")
		tested++
	} else {
		log.Print("telegram: skipped (TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID not set)")
	}

	if key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); key != "" {
		j, err := NewJev(envOr("TYPESAFE_URL", "https://api.typesafe.ai"), key, envOr("FLIP_JEV_MODEL", "jev-latest"))
		if err != nil {
			return err
		}
		t := Target{Name: "PS5 console", MarketPrice: 320}
		d := Deal{Target: t.Name, Price: 210, Listing: Listing{
			Title:            "Sony PlayStation 5 Disc Edition console, boxed with controller",
			ShortDescription: "Fully working, factory reset, original box and cables.",
			Condition:        "Used",
		}}
		a, err := j.Check(ctx, t, d)
		if err != nil {
			return fmt.Errorf("jev test failed: %w", err)
		}
		keep, verdict, _ := Decide(a)
		log.Printf("jev: answered the test listing: %+v (keep=%v, %s)", a, keep, verdict)
		tested++
	} else {
		log.Print("jev: skipped (TYPESAFE_API_KEY not set)")
	}

	if tested == 0 {
		return fmt.Errorf("nothing to test: set the Telegram and/or TYPESAFE_API_KEY variables")
	}
	return nil
}
