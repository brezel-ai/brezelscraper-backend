package services

import (
	"context"
	"log/slog"
	"math"
	"testing"
)

// 1 keyword, depth 5 → 40 places (the shared baseline used across the
// estimation tests). The Apify-style website filter adds 0.001 per matching
// place, so an active filter must add exactly places × 0.001 to the breakdown
// and total, and an inactive filter ("" / "all") must add nothing.
func TestEstimateJobCost_WebsiteFilter_AddsPerPlaceFee(t *testing.T) {
	t.Parallel()

	svc := NewEstimationService(nil, nil, slog.Default())
	ctx := context.Background()

	base, err := svc.EstimateJobCost(ctx, []string{"Cafe"}, 5, nil, false, intPtr(0), intPtr(0), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.Places != 40 {
		t.Fatalf("Places = %d, want 40 (baseline this test depends on)", base.Places)
	}
	if base.Breakdown.FilterCost != 0 {
		t.Errorf(`FilterCost = %.4f for "" filter, want 0`, base.Breakdown.FilterCost)
	}

	for _, filter := range []string{"no_website", "has_website"} {
		filter := filter
		t.Run(filter, func(t *testing.T) {
			t.Parallel()
			est, err := svc.EstimateJobCost(ctx, []string{"Cafe"}, 5, nil, false, intPtr(0), intPtr(0), filter)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantFilter := float64(est.Places) * 0.001
			if math.Abs(est.Breakdown.FilterCost-wantFilter) > 1e-9 {
				t.Errorf("FilterCost = %.4f, want %.4f (places × 0.001)", est.Breakdown.FilterCost, wantFilter)
			}
			// The filter fee must show up in the total, not just the breakdown.
			if math.Abs((est.Total-base.Total)-wantFilter) > 1e-9 {
				t.Errorf("Total delta = %.4f, want %.4f (filter fee added on top of base)",
					est.Total-base.Total, wantFilter)
			}
			if _, ok := est.UnitPrices["filters_applied"]; !ok {
				t.Error(`UnitPrices missing "filters_applied" key`)
			}
		})
	}

	// "all" is an explicit no-op filter and must behave like "".
	allEst, err := svc.EstimateJobCost(ctx, []string{"Cafe"}, 5, nil, false, intPtr(0), intPtr(0), "all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allEst.Breakdown.FilterCost != 0 {
		t.Errorf(`FilterCost = %.4f for "all" filter, want 0`, allEst.Breakdown.FilterCost)
	}
}
