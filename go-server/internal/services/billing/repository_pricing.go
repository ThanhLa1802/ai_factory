package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pricingRepo struct{ db *gorm.DB }

func (r *pricingRepo) Upsert(ctx context.Context, p *Price) error {
	row := pricingRow{
		ID:                          uuid.NewString(),
		Model:                       p.Model,
		Currency:                    p.Currency,
		PricePerMillionInputTokens:  p.PricePerMillionInputTokens,
		PricePerMillionOutputTokens: p.PricePerMillionOutputTokens,
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "model"}, {Name: "currency"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"price_per_million_input_tokens",
			"price_per_million_output_tokens",
			"updated_at",
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert price: %w", err)
	}
	return nil
}

func (r *pricingRepo) Get(ctx context.Context, model, currency string) (*Price, error) {
	var row pricingRow
	err := r.db.WithContext(ctx).
		Where("model = ? AND currency = ?", model, currency).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoPrice
	}
	if err != nil {
		return nil, fmt.Errorf("get price: %w", err)
	}
	p := toPrice(row)
	return &p, nil
}

func (r *pricingRepo) List(ctx context.Context) ([]Price, error) {
	var rows []pricingRow
	if err := r.db.WithContext(ctx).Order("model").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}
	out := make([]Price, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPrice(row))
	}
	return out, nil
}
