package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
		items, err := c.Search(context.Background(), "ps5", 300)
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
