package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Notifier interface {
	Send(ctx context.Context, d Deal) error
	// SendIndex lists a run's deals ahead of the alerts; held is how many more
	// qualified but were over the per-run limit.
	SendIndex(ctx context.Context, deals []Deal, held int) error
}

// Telegram sends an index for each run, then one message per deal, to your chat via a bot.
type Telegram struct {
	Token, ChatID string
	HTTP          *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Telegram) Send(ctx context.Context, d Deal) error {
	return t.SendText(ctx, formatDeal(d, true))
}

func (t *Telegram) SendIndex(ctx context.Context, deals []Deal, held int) error {
	return t.SendText(ctx, formatIndex(deals, held, true))
}

// SendText sends one HTML-formatted message, e.g. a -ping test.
func (t *Telegram) SendText(ctx context.Context, text string) error {
	form := url.Values{
		"chat_id":    {t.ChatID},
		"text":       {text},
		"parse_mode": {"HTML"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed") // don't print the URL: it contains the bot token
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("telegram: %s %s", resp.Status, body)
	}
	return nil
}

// Console prints deals to stdout (dry run / demo).
type Console struct{}

func (Console) Send(_ context.Context, d Deal) error {
	fmt.Println(formatDeal(d, false))
	fmt.Println(strings.Repeat("─", 60))
	return nil
}

func (Console) SendIndex(_ context.Context, deals []Deal, held int) error {
	fmt.Println(formatIndex(deals, held, false))
	fmt.Println(strings.Repeat("═", 60))
	return nil
}

// advice is the one-word call shown on each alert. Anything with a warning, or
// that Jev hasn't confirmed, needs a human look before buying.
func advice(d Deal) (icon, label string) {
	switch {
	case d.Caution || d.Verdict == "":
		return "🟡", "Check first"
	case d.Auction:
		return "🟢", "Bid"
	}
	return "🟢", "Buy"
}

func formatDeal(d Deal, htmlMode bool) string {
	esc := func(s string) string {
		if htmlMode {
			return html.EscapeString(s)
		}
		return s
	}
	icon, label := advice(d)
	head := fmt.Sprintf("#%d/%d · %s %s · %s", d.Rank, d.Total, icon, label, d.Target)
	if htmlMode {
		head = "<b>" + esc(head) + "</b>"
	}

	priceLabel, postage := "Price", fmt.Sprintf("£%.2f", d.Shipping)
	if d.Auction {
		priceLabel = "Bid"
	}
	if _, ok := cheapestShipping(d.Listing); !ok {
		postage = "check"
		if d.Listing.NearPickup {
			postage = "collect"
		}
	}
	rows := [][2]string{
		{priceLabel, fmt.Sprintf("£%.2f", d.Price)},
		{"Postage", postage},
		{"Total", fmt.Sprintf("£%.2f", d.Cost)},
	}
	if d.ForMe {
		if d.BuyUnder > 0 {
			rows = append(rows, [2]string{"Limit", fmt.Sprintf("£%.0f (£%.0f under)", d.BuyUnder, d.BuyUnder-d.Cost)})
		}
		rows = append(rows, [2]string{"Typical", fmt.Sprintf("~£%.0f (%s)", d.Resale, versusTypical(d))})
	} else {
		rows = append(rows,
			[2]string{"Resale", fmt.Sprintf("~£%.0f", d.Resale)},
			[2]string{"Profit", fmt.Sprintf("~£%.0f (%.0f%% ROI)", d.Profit, d.ROI*100)})
	}
	rows = append(rows, [][2]string{
		{"Score", fmt.Sprintf("%.0f", d.Score)},
		{"Advice", label},
	}...)
	if c := d.Listing.Condition; c != "" {
		rows = append(rows, [2]string{"Condition", c})
	}
	if loc := d.Listing.ItemLocation; loc != nil && loc.City != "" {
		rows = append(rows, [2]string{"Location", loc.City})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n", head, esc(d.Listing.Title))
	if htmlMode {
		b.WriteString("<pre>")
	}
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%-9s %s", r[0], esc(r[1]))
	}
	if htmlMode {
		b.WriteString("</pre>")
	}
	b.WriteString("\n")
	if d.Verdict != "" {
		fmt.Fprintf(&b, "%s\n", esc(d.Verdict))
	}
	for _, f := range d.Flags {
		fmt.Fprintf(&b, "%s\n", esc(f))
	}
	if htmlMode {
		fmt.Fprintf(&b, `<a href="%s">Open on eBay</a>`, html.EscapeString(d.Listing.ItemWebURL))
	} else {
		b.WriteString(d.Listing.ItemWebURL)
	}
	return b.String()
}

// formatIndex is the contents page for a run: one numbered entry per alert,
// using the same numbers, in the same order.
func formatIndex(deals []Deal, held int, htmlMode bool) string {
	noun := "deals"
	if len(deals) == 1 {
		noun = "deal"
	}
	head := fmt.Sprintf("🔎 flipfinder: %d new %s", len(deals), noun)
	var b strings.Builder
	if htmlMode {
		head = "<b>" + html.EscapeString(head) + "</b>"
	}
	b.WriteString(head)
	for _, d := range deals {
		icon, label := advice(d)
		name := d.Target
		if htmlMode {
			name = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(d.Listing.ItemWebURL), html.EscapeString(d.Target))
		}
		value := fmt.Sprintf("~£%.0f profit", d.Profit)
		switch {
		case d.BuyUnder > 0:
			value = fmt.Sprintf("£%.0f under your limit", d.BuyUnder-d.Cost)
		case d.ForMe:
			value = versusTypical(d)
		}
		fmt.Fprintf(&b, "\n\n%d. %s %s · %s\n£%.0f total · %s · score %.0f",
			d.Rank, icon, label, name, d.Cost, value, d.Score)
	}
	if held > 0 {
		fmt.Fprintf(&b, "\n\n+%d more next run", held)
	}
	return b.String()
}

// Store remembers what we've already alerted on, so you get each deal once
// (and again only if the price drops by 5% or more).
type Store struct {
	path string
	Seen map[string]seenEntry `json:"seen"`
}

type seenEntry struct {
	Price float64   `json:"price"`
	At    time.Time `json:"at"`
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, Seen: map[string]seenEntry{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Seen == nil {
		s.Seen = map[string]seenEntry{}
	}
	return s, nil
}

func (s *Store) ShouldAlert(id string, price float64) bool {
	e, ok := s.Seen[id]
	return !ok || price <= e.Price*0.95
}

func (s *Store) Mark(id string, price float64, now time.Time) {
	s.Seen[id] = seenEntry{Price: price, At: now}
}

// Save prunes entries older than 21 days and writes the file atomically.
func (s *Store) Save(now time.Time) error {
	for id, e := range s.Seen {
		if now.Sub(e.At) > 21*24*time.Hour {
			delete(s.Seen, id)
		}
	}
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// versusTypical says how a buy-for-yourself price compares with the typical one.
func versusTypical(d Deal) string {
	if d.Profit >= 0 {
		return fmt.Sprintf("save ~£%.0f", d.Profit)
	}
	return fmt.Sprintf("~£%.0f above typical", -d.Profit)
}
