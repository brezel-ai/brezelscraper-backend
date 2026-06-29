package webpresence

import "testing"

func TestKeep(t *testing.T) {
	cases := []struct {
		filter string
		tier   Tier
		want   bool
	}{
		{FilterAll, TierHas, true}, {FilterAll, TierNone, true}, {"", TierHas, true},
		{FilterNoWebsite, TierNone, true}, {FilterNoWebsite, TierSocial, true},
		{FilterNoWebsite, TierBooking, true}, {FilterNoWebsite, TierHas, false},
		{FilterHasWebsite, TierHas, true}, {FilterHasWebsite, TierNone, false},
		{FilterHasWebsite, TierSocial, false},
	}
	for _, c := range cases {
		if got := Keep(c.filter, c.tier); got != c.want {
			t.Errorf("Keep(%q,%q)=%v want %v", c.filter, c.tier, got, c.want)
		}
	}
}

func TestIsValidFilterAndApplied(t *testing.T) {
	for _, ok := range []string{"", "all", "no_website", "has_website"} {
		if !IsValidFilter(ok) {
			t.Errorf("IsValidFilter(%q)=false", ok)
		}
	}
	if IsValidFilter("bogus") {
		t.Error("IsValidFilter(bogus)=true")
	}
	if FilterApplied("all") || FilterApplied("") {
		t.Error("FilterApplied(all/empty) should be false")
	}
	if !FilterApplied("no_website") || !FilterApplied("has_website") {
		t.Error("FilterApplied(no_website/has_website) should be true")
	}
}

func TestKeepWebsite(t *testing.T) {
	if KeepWebsite(FilterNoWebsite, "https://acme.com") {
		t.Error("real site should be dropped under no_website")
	}
	if !KeepWebsite(FilterNoWebsite, "https://facebook.com/acme") {
		t.Error("social should be kept under no_website")
	}
}
