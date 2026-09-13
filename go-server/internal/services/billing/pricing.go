package billing

import (
	"context"
	"strings"
)

// Price is a model's unit price. Prices are integer micro-credits (µcr) per
// 1,000,000 tokens, priced separately for input and output. Money never uses
// float.
type Price struct {
	Model                       string `json:"model"`
	Currency                    string `json:"currency"`
	PricePerMillionInputTokens  int64  `json:"price_per_million_input_tokens"`
	PricePerMillionOutputTokens int64  `json:"price_per_million_output_tokens"`
}

// costMicro converts a token count into micro-credits, rounding half-up to the
// nearest µcr.
func costMicro(tokens int, pricePerMillion int64) int64 {
	if tokens <= 0 || pricePerMillion <= 0 {
		return 0
	}
	return (int64(tokens)*pricePerMillion + 500_000) / 1_000_000
}

// UpsertPrice creates or replaces the price for (model, currency).
func (s *Service) UpsertPrice(ctx context.Context, p Price) (*Price, error) {
	p.Model = strings.TrimSpace(p.Model)
	if p.Currency == "" {
		p.Currency = s.cfg.Currency
	}
	if p.Model == "" || p.PricePerMillionInputTokens < 0 || p.PricePerMillionOutputTokens < 0 {
		return nil, ErrInvalidPrice
	}
	if err := s.repos.Pricing.Upsert(ctx, &p); err != nil {
		return nil, err
	}
	return s.repos.Pricing.Get(ctx, p.Model, p.Currency)
}

// ListPrices returns every configured model price.
func (s *Service) ListPrices(ctx context.Context) ([]Price, error) {
	return s.repos.Pricing.List(ctx)
}

// priceFor returns the model's price or ErrNoPrice.
func (s *Service) priceFor(ctx context.Context, model string) (Price, error) {
	p, err := s.repos.Pricing.Get(ctx, model, s.cfg.Currency)
	if err != nil {
		return Price{}, err
	}
	return *p, nil
}
