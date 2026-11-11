package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEbayBaseURL(t *testing.T) {
	for env, want := range map[string]string{
		"":           "https://api.ebay.com",
		"production": "https://api.ebay.com",
		"Sandbox":    "https://api.sandbox.ebay.com",
	} {
		if got, err := EbayBaseURL(env); err != nil || got != want {
			t.Errorf("EbayBaseURL(%q) = %q, %v; want %q", env, got, err, want)
		}
	}
	if _, err := EbayBaseURL("staging"); err == nil {
		t.Error("EbayBaseURL(staging) should fail")
	}
}

func TestEbayClientUsesBaseURLAndCachesToken(t *testing.T) {
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/v1/oauth2/token":
			tokenCalls++
			if u, p, ok := r.BasicAuth(); !ok || u != "id" || p != "secret" {
				t.Errorf("token request auth = %q/%q", u, p)
			}
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
		case "/buy/browse/v1/item_summary/search":
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("X-EBAY-C-MARKETPLACE-ID"); got != "EBAY_GB" {
				t.Errorf("marketplace = %q", got)
			}
			w.Write([]byte(`{"itemSummaries":[{"itemId":"1","title":"PS5","price":{"value":"200.00","currency":"GBP"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "")
	for i := 0; i < 2; i++ {
		items, err := c.Search(context.Background(), "ps5", SearchOptions{MaxPrice: 300})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Title != "PS5" {
			t.Fatalf("items = %+v", items)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("token fetched %d times, want 1", tokenCalls)
	}
}

func TestEbaySearchRetriesWhenRateLimited(t *testing.T) {
	var searches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		searches++
		if searches == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"itemSummaries":[{"itemId":"1","title":"PS5"}]}`))
	}))
	defer srv.Close()

	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "")
	items, err := c.Search(context.Background(), "ps5", SearchOptions{MaxPrice: 300})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if searches != 2 {
		t.Errorf("searches = %d, want 2 (one retry)", searches)
	}
}

func TestRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"": time.Second, "abc": time.Second, "3": 3 * time.Second, "999": 10 * time.Second, "-1": time.Second,
	} {
		if got := retryAfter(in, time.Second); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
func TestEbaySearchPriceFilterRoundsUp(t *testing.T) {
	var filter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		filter = r.URL.Query().Get("filter")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "")
	if _, err := c.Search(context.Background(), "ps5", SearchOptions{MaxPrice: 239.4}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filter, "price:[..240]") {
		t.Errorf("filter = %q, want a price cap of 240 so a £239.40 listing is not dropped", filter)
	}
}

func TestEbaySearchNewOnly(t *testing.T) {
	var filter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		filter = r.URL.Query().Get("filter")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "")
	if _, err := c.Search(context.Background(), "tyre", SearchOptions{MaxPrice: 120, NewOnly: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filter, "conditionIds:{1000|1500}") {
		t.Errorf("filter = %q, want only New and New-other", filter)
	}
}

func TestEbaySearchPickupRadius(t *testing.T) {
	var filter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		filter = r.URL.Query().Get("filter")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "TW3")
	opt := SearchOptions{MaxPrice: 600, NewOnly: true, PickupRadiusMiles: 200, PickupPostcode: "TW3"}
	if _, err := c.Search(context.Background(), "tyre", opt); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"deliveryOptions:{SELLER_ARRANGED_LOCAL_PICKUP}", "pickupPostalCode:TW3", "pickupRadius:200", "pickupRadiusUnit:mi"} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter %q is missing %q", filter, want)
		}
	}
}

func TestEbaySearchNoPriceCap(t *testing.T) {
	var filter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		filter = r.URL.Query().Get("filter")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEbayClient("id", "secret", srv.URL, "EBAY_GB", "")
	if _, err := c.Search(context.Background(), "tyre", SearchOptions{NewOnly: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filter, "price:") {
		t.Errorf("filter = %q, want no price cap for any-price targets", filter)
	}
}
