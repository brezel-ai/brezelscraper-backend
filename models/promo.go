package models

import (
	"errors"
	"time"
)

// PromoCode is a campaign code that grants free credits on redemption.
type PromoCode struct {
	ID                 string     `json:"id"`
	Code               string     `json:"code"`
	Amount             float64    `json:"amount"`
	Description        *string    `json:"description,omitempty"`
	Status             string     `json:"status"` // "active" | "disabled"
	MaxRedemptions     *int       `json:"max_redemptions,omitempty"`
	CurrentRedemptions int        `json:"current_redemptions"`
	NewAccountsOnly    bool       `json:"new_accounts_only"`
	ValidFrom          time.Time  `json:"valid_from"`
	ValidTo            *time.Time `json:"valid_to,omitempty"`
	CreatedBy          *string    `json:"created_by,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// RedeemResult is returned to the client after a successful redemption.
// Amounts are strings to avoid float drift on the wire (matches the credit API).
type RedeemResult struct {
	CreditsAdded string `json:"credits_added"`
	NewBalance   string `json:"new_balance"`
}

// CreatePromoCodeRequest is the admin create payload.
type CreatePromoCodeRequest struct {
	Code            string     `json:"code"`
	Amount          float64    `json:"amount"`
	Description     *string    `json:"description"`
	MaxRedemptions  *int       `json:"max_redemptions"`
	NewAccountsOnly bool       `json:"new_accounts_only"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidTo         *time.Time `json:"valid_to"`
}

// Promo redemption sentinel errors. The service maps these to HTTP statuses.
var (
	ErrPromoNotFound        = errors.New("promo code not found")
	ErrPromoDisabled        = errors.New("promo code disabled")
	ErrPromoNotYetActive    = errors.New("promo code not yet active")
	ErrPromoExpired         = errors.New("promo code expired")
	ErrPromoExhausted       = errors.New("promo code fully redeemed")
	ErrPromoAlreadyRedeemed = errors.New("promo code already redeemed by this user")
	ErrPromoNewAccountsOnly = errors.New("promo code is for new accounts only")
	ErrPromoCodeExists      = errors.New("promo code already exists")
)
