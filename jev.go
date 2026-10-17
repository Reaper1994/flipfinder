package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Checker judges a listing that already passed the price rules.
type Checker interface {
	Check(ctx context.Context, t Target, d Deal) (ListingAnswers, error)
}

// Jev calls TypeSafe's System One API (POST /v1/systemone), the same way Sift
// does: several small independent questions in one request, answers combined
// in plain Go (Decide) so the policy is testable without touching prompts.
type Jev struct {
	BaseURL, APIKey, Model string
	HTTP                   *http.Client
	MaxRetries             int
}

func NewJev(baseURL, apiKey, model string) (*Jev, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "https://") {
		return nil, errors.New("TYPESAFE_URL must be https")
	}
	return &Jev{BaseURL: baseURL, APIKey: apiKey, Model: model,
		HTTP: &http.Client{Timeout: 20 * time.Second}, MaxRetries: 3}, nil
}

type jevChoice struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}
type jevScore struct {
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}
type jevNoul struct {
	Noul float64 `json:"noul"`
}

// ListingAnswers is the typed result for one listing.
type ListingAnswers struct {
	Matches   jevNoul  // is it the product we're hunting (right model/generation)?
	Genuine   jevNoul  // the working item itself, not a box/part/wanted ad?
	Risk      jevNoul  // scam, stolen or locked-device signals?
	Condition jevScore // 0 faulty … 3 like new / boxed complete
}

var jevQuestions = map[string]any{
	"matches": map[string]any{
		"type":         "noul",
		"instructions": "Is this listing for the target product (same model and generation), rather than a different or older model?",
	},
	"genuine": map[string]any{
		"type":         "noul",
		"instructions": "Is the seller offering the complete, working item itself, rather than only a box, case, controller, cable, spare part, photo of the item, or a 'wanted' request?",
	},
	"risk": map[string]any{
		"type":         "noul",
		"instructions": "Does anything suggest a scam, stolen goods, or a device the buyer can't use: iCloud/activation lock, account or console ban, blacklisted, 'no questions', payment or contact outside eBay, a price far below market with a vague description, or a brand-new seller with stock photos?",
	},
	"condition": map[string]any{
		"type":         "score",
		"instructions": "What condition is the item in, judging only from the listing?",
		"criteria": []string{
			"Faulty, damaged, or condition unknown",
			"Heavy wear, or missing essential parts such as the power cable or controller",
			"Good used condition and fully working",
			"Like new, or boxed and complete",
		},
	},
}

func (j *Jev) Check(ctx context.Context, t Target, d Deal) (ListingAnswers, error) {
	l := d.Listing
	state := map[string]any{
		"target_product":  t.Name,
		"typical_resale":  fmt.Sprintf("£%.0f", t.MarketPrice),
		"title":           sanitize(l.Title),
		"description":     sanitize(l.ShortDescription),
		"condition":       l.Condition,
		"price":           fmt.Sprintf("£%.2f", d.Price),
		"is_auction":      d.Auction,
		"seller_feedback": sellerFeedback(l),
		"listed":          l.ItemCreationDate,
	}
	payload, err := json.Marshal(map[string]any{"state": state, "model": j.Model, "questions": jevQuestions})
	if err != nil {
		return ListingAnswers{}, err
	}
	var raw struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := j.post(ctx, payload, &raw); err != nil {
		return ListingAnswers{}, err
	}
	var a ListingAnswers
	for key, dst := range map[string]any{
		"matches": &a.Matches, "genuine": &a.Genuine, "risk": &a.Risk, "condition": &a.Condition,
	} {
		msg, ok := raw.Answers[key]
		if !ok {
			return ListingAnswers{}, fmt.Errorf("jev: missing answer %q", key)
		}
		if err := json.Unmarshal(msg, dst); err != nil {
			return ListingAnswers{}, fmt.Errorf("jev: bad answer %q", key)
		}
	}
	return a, nil
}

// Decide applies the buying policy to Jev's answers. It returns whether to
// keep the deal, a short verdict for the alert, and a score multiplier.
func Decide(a ListingAnswers) (keep bool, verdict string, mult float64) {
	switch {
	case a.Risk.Noul >= 0.6:
		return false, fmt.Sprintf("risky listing (%.0f%%)", a.Risk.Noul*100), 0
	case a.Matches.Noul < 0.5:
		return false, "not the target model", 0
	case a.Genuine.Noul < 0.5:
		return false, "not the full working item", 0
	case a.Condition.Score < 1:
		return false, "faulty or unknown condition", 0
	}
	mult = 1.0
	verdict = "✅ looks genuine"
	if a.Condition.Score < 2 {
		verdict = "🟡 genuine but worn or missing parts — budget for replacements"
		mult = 0.7
	} else if a.Condition.Score >= 2.5 {
		verdict = "✅ looks genuine, very good condition"
		mult = 1.1
	}
	if a.Risk.Noul >= 0.3 {
		verdict += fmt.Sprintf(" · some risk signals (%.0f%%), ask questions first", a.Risk.Noul*100)
		mult *= 0.8
	}
	return true, verdict, mult
}

func (j *Jev) post(ctx context.Context, payload []byte, out any) error {
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.BaseURL+"/v1/systemone", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+j.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := j.HTTP.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = fmt.Errorf("jev request failed: %v", ue.Err)
			}
			return err
		}
		if resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			return json.NewDecoder(resp.Body).Decode(out)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 || resp.StatusCode >= 500
		if !retryable || attempt >= j.MaxRetries {
			return fmt.Errorf("jev: %s", resp.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func sellerFeedback(l Listing) string {
	if l.Seller == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d ratings, %s%% positive", l.Seller.FeedbackScore, l.Seller.FeedbackPercentage)
}

// sanitize drops control characters from listing text.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
