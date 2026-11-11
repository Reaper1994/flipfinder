package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SearchOptions narrow one eBay search.
type SearchOptions struct {
	MaxPrice float64
	NewOnly  bool // New and New-other only
	// PickupRadiusMiles > 0: only listings you can collect within this many
	// miles of PickupPostcode (eBay's local-pickup search).
	PickupRadiusMiles float64
	PickupPostcode    string
}

// Searcher is anything that returns listings for a query (eBay, or the demo fake).
type Searcher interface {
	Search(ctx context.Context, query string, opt SearchOptions) ([]Listing, error)
}

// Listing is the subset of an eBay item summary we use.
type Listing struct {
	// NearPickup is set when eBay's local-pickup search returned this listing,
	// i.e. it can be collected within the target's pickup radius. Not from eBay.
	NearPickup bool `json:"-"`

	ItemID           string   `json:"itemId"`
	Title            string   `json:"title"`
	Price            *Money   `json:"price"`
	CurrentBidPrice  *Money   `json:"currentBidPrice"`
	BidCount         int      `json:"bidCount"`
	BuyingOptions    []string `json:"buyingOptions"`
	ItemWebURL       string   `json:"itemWebUrl"`
	ItemEndDate      string   `json:"itemEndDate"`
	ItemCreationDate string   `json:"itemCreationDate"`
	Condition        string   `json:"condition"`
	ShortDescription string   `json:"shortDescription"`
	Image            *struct {
		ImageURL string `json:"imageUrl"`
	} `json:"image"`
	ShippingOptions []struct {
		ShippingCost     *Money `json:"shippingCost"`
		ShippingCostType string `json:"shippingCostType"`
	} `json:"shippingOptions"`
	ItemLocation *struct {
		City       string `json:"city"`
		PostalCode string `json:"postalCode"`
	} `json:"itemLocation"`
	Seller *struct {
		Username           string `json:"username"`
		FeedbackPercentage string `json:"feedbackPercentage"`
		FeedbackScore      int    `json:"feedbackScore"`
	} `json:"seller"`
}

type Money struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

func (m *Money) Float() (float64, bool) {
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m.Value, 64)
	return f, err == nil
}

// EbayClient uses the official Browse API with an application token
// (client-credentials grant). No scraping, no login.
type EbayClient struct {
	ClientID, ClientSecret string
	BaseURL                string // production or sandbox API host, see EbayBaseURL
	Marketplace            string
	Postcode               string
	HTTP                   *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// EbayBaseURL maps EBAY_ENV to an API host. Empty means production; sandbox
// keys (their IDs contain "-SBX-") only work against the sandbox host.
func EbayBaseURL(env string) (string, error) {
	switch strings.ToLower(env) {
	case "", "production", "prod":
		return "https://api.ebay.com", nil
	case "sandbox":
		return "https://api.sandbox.ebay.com", nil
	}
	return "", fmt.Errorf("EBAY_ENV must be production or sandbox, got %q", env)
}

func NewEbayClient(id, secret, baseURL, marketplace, postcode string) *EbayClient {
	return &EbayClient{
		ClientID: id, ClientSecret: secret, BaseURL: baseURL, Marketplace: marketplace, Postcode: postcode,
		HTTP: &http.Client{Transport: newTransport(), Timeout: 20 * time.Second},
	}
}

// newTransport returns a transport that keeps enough idle connections per host
// for concurrent requests to reuse, instead of reconnecting (TLS handshake
// included) once more than the default two are in flight.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 8
	return t
}

func (c *EbayClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {"https://api.ebay.com/oauth/api_scope"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/identity/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.ClientID+":"+c.ClientSecret)))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// eBay's error body (e.g. invalid_client) names the cause and holds no secrets.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("ebay token: %s %s (check EBAY_CLIENT_ID / EBAY_CLIENT_SECRET, and set EBAY_ENV=sandbox for sandbox keys)",
			resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	c.token = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 5*time.Minute)
	return c.token, nil
}

// Search returns the newest UK listings for query priced up to maxPrice,
// excluding eBay's "for parts or not working" condition. See SearchOptions.
func (c *EbayClient) Search(ctx context.Context, query string, opt SearchOptions) ([]Listing, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	// New, open box, refurbished and used grades; 7000 (for parts) left out.
	conditions := "conditionIds:{1000|1500|2000|2010|2020|2030|2500|2750|3000|4000|5000|6000}"
	if opt.NewOnly {
		conditions = "conditionIds:{1000|1500}"
	}
	var filters []string
	if opt.MaxPrice > 0 {
		filters = append(filters, fmt.Sprintf("price:[..%.0f]", math.Ceil(opt.MaxPrice)))
	}
	filters = append(filters,
		"priceCurrency:GBP",
		"itemLocationCountry:GB",
		"buyingOptions:{FIXED_PRICE|AUCTION|BEST_OFFER}",
		conditions,
	)
	if opt.PickupRadiusMiles > 0 && opt.PickupPostcode != "" {
		filters = append(filters,
			"deliveryOptions:{SELLER_ARRANGED_LOCAL_PICKUP}",
			"pickupCountry:GB",
			"pickupPostalCode:"+opt.PickupPostcode,
			fmt.Sprintf("pickupRadius:%.0f", opt.PickupRadiusMiles),
			"pickupRadiusUnit:mi",
		)
	}
	filter := strings.Join(filters, ",")
	q := url.Values{
		"q":      {query},
		"filter": {filter},
		"sort":   {"newlyListed"},
		"limit":  {"50"},
	}
	u := c.BaseURL + "/buy/browse/v1/item_summary/search?" + q.Encode()
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-EBAY-C-MARKETPLACE-ID", c.Marketplace)
		if c.Postcode != "" {
			req.Header.Set("X-EBAY-C-ENDUSERCTX", "contextualLocation="+url.QueryEscape("country=GB,zip="+c.Postcode))
		}
		resp, err = c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= maxSearchRetries {
			break
		}
		// Rate limited: wait as long as eBay asks, then try again.
		wait := retryAfter(resp.Header.Get("Retry-After"), time.Duration(attempt+1)*time.Second)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("ebay search %q: %s %s", query, resp.Status, body)
	}
	var out struct {
		ItemSummaries []Listing `json:"itemSummaries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.ItemSummaries, nil
}

const maxSearchRetries = 2

// retryAfter parses a Retry-After header given in seconds, capped at 10s so a
// run never stalls for long; def is used when it is missing or malformed.
func retryAfter(h string, def time.Duration) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || secs < 0 {
		return def
	}
	return min(time.Duration(secs)*time.Second, 10*time.Second)
}
