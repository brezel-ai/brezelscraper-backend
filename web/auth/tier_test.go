package auth_test

import (
	"context"
	"testing"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

// TestGetUserTier locks in the safe-default contract: a context with no
// UserTierKey set must report free tier, NOT empty. Granting paid limits to
// an unauthenticated or improperly-wired request is the only failure mode
// we want to rule out at the call site of the rate limiter.
func TestGetUserTier(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "no value set defaults to free",
			ctx:  context.Background(),
			want: models.UserTierFree,
		},
		{
			name: "explicit free is free",
			ctx:  context.WithValue(context.Background(), auth.UserTierKey, models.UserTierFree),
			want: models.UserTierFree,
		},
		{
			name: "explicit paid is paid",
			ctx:  context.WithValue(context.Background(), auth.UserTierKey, models.UserTierPaid),
			want: models.UserTierPaid,
		},
		{
			name: "empty string defaults to free (not propagated as paid)",
			ctx:  context.WithValue(context.Background(), auth.UserTierKey, ""),
			want: models.UserTierFree,
		},
		{
			name: "wrong type defaults to free",
			ctx:  context.WithValue(context.Background(), auth.UserTierKey, 123),
			want: models.UserTierFree,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auth.GetUserTier(tt.ctx); got != tt.want {
				t.Errorf("GetUserTier() = %q, want %q", got, tt.want)
			}
		})
	}
}
