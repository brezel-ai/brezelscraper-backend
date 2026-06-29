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

func TestParseQueryFilter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // nil means "no filter"
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"all alias is no filter", "all", nil},
		{"all mixed with tiers is no filter", "all,none,has", nil},
		{"no_website group expands", "no_website", []string{"none", "social", "directory", "builder", "booking"}},
		{"has_website group expands", "has_website", []string{"has"}},
		{"single tier", "none", []string{"none"}},
		{"multiple tiers", "social,none", []string{"none", "social"}}, // canonical order
		{"group + tier union dedup", "no_website,social,has", []string{"none", "social", "directory", "builder", "booking", "has"}},
		{"duplicate tiers deduped", "none,none,social", []string{"none", "social"}},
		{"case-insensitive group", "NO_WEBSITE", []string{"none", "social", "directory", "builder", "booking"}},
		{"case-insensitive tier", "Has", []string{"has"}},
		{"whitespace around tokens", " none , social ", []string{"none", "social"}},
		{"empty tokens skipped", "none,,social,", []string{"none", "social"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseQueryFilter(c.in)
			if err != nil {
				t.Fatalf("ParseQueryFilter(%q) unexpected error: %v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("ParseQueryFilter(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("ParseQueryFilter(%q) = %v, want %v", c.in, got, c.want)
				}
			}
		})
	}

	for _, bad := range []string{"bogus", "no_website,bogus", "website", "none,nope"} {
		if _, err := ParseQueryFilter(bad); err == nil {
			t.Errorf("ParseQueryFilter(%q) expected error, got nil", bad)
		}
	}
}
