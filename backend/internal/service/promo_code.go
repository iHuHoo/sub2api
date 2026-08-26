package service

import (
	"time"
)

// PromoCode 注册优惠码
type PromoCode struct {
	ID           int64
	Code         string
	Purpose      string
	DiscountRate *float64
	BonusAmount  float64
	MaxUses      int
	UsedCount    int
	Status       string
	ExpiresAt    *time.Time
	Notes        string
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// 关联
	UsageRecords []PromoCodeUsage
}

// PromoCodeUsage 优惠码使用记录
type PromoCodeUsage struct {
	ID             int64
	PromoCodeID    int64
	UserID         int64
	PaymentOrderID *int64
	UsageType      string
	Status         string
	BonusAmount    float64
	DiscountAmount float64
	UsedAt         time.Time
	ReservedAt     *time.Time
	ConsumedAt     *time.Time
	ReleasedAt     *time.Time

	// 关联
	PromoCode *PromoCode
	User      *User
}

// CanUse 检查优惠码是否可用
func (p *PromoCode) CanUse() bool {
	if p.Status != PromoCodeStatusActive {
		return false
	}
	if p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

// IsExpired 检查是否已过期
func (p *PromoCode) IsExpired() bool {
	return p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt)
}

// CreatePromoCodeInput 创建优惠码输入
type CreatePromoCodeInput struct {
	Code         string
	Purpose      string
	DiscountRate *float64
	BonusAmount  float64
	MaxUses      int
	ExpiresAt    *time.Time
	Notes        string
}

// UpdatePromoCodeInput 更新优惠码输入
type UpdatePromoCodeInput struct {
	Code         *string
	Purpose      *string
	DiscountRate *float64
	BonusAmount  *float64
	MaxUses      *int
	Status       *string
	ExpiresAt    *time.Time
	Notes        *string
}

type SubscriptionPromoPreview struct {
	Code             string  `json:"code"`
	DiscountRate     float64 `json:"discount_rate"`
	OriginalAmount   float64 `json:"original_amount"`
	DiscountAmount   float64 `json:"discount_amount"`
	DiscountedAmount float64 `json:"discounted_amount"`
}
