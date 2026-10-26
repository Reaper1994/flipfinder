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
}

// Telegram sends one message per deal to your chat via a bot.
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

func formatDeal(d Deal, htmlMode bool) string {
	esc := func(s string) string {
		if htmlMode {
			return html.EscapeString(s)
		}
		return s
	}
	bold := func(s string) string {
		if htmlMode {
			return "<b>" + esc(s) + "</b>"
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  ~£%.0f profit (%.0f%% ROI)\n", bold("💷 "+d.Target), d.Profit, d.ROI*100)
	fmt.Fprintf(&b, "%s\n", esc(d.Listing.Title))
	cost := fmt.Sprintf("Buy £%.2f", d.Price)
	if d.Shipping > 0 {
		cost += fmt.Sprintf(" + £%.2f post", d.Shipping)
	}
	fmt.Fprintf(&b, "%s → resell ~£%.0f\n", cost, d.Resale)
	if c := d.Listing.Condition; c != "" {
		fmt.Fprintf(&b, "Condition: %s", esc(c))
		if loc := d.Listing.ItemLocation; loc != nil && loc.City != "" {
			fmt.Fprintf(&b, " · %s", esc(loc.City))
		}
		b.WriteString("\n")
	}
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
