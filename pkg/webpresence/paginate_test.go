package webpresence

import "testing"

func rows() []Row {
	// 5 rows in display order; tiers: none, has, social, none, builder
	return []Row{
		{ID: 1, Website: ""},
		{ID: 2, Website: "https://acme.com"},
		{ID: 3, Website: "https://facebook.com/x"},
		{ID: 4, Website: ""},
		{ID: 5, Website: "https://x.wixsite.com/y"},
	}
}

func TestPaginate_CountsAllTiers(t *testing.T) {
	got := Paginate(rows(), nil, 50, 0)

	// counts must include every tier, zero-filled
	for _, tier := range AllTiers() {
		if _, ok := got.Counts[string(tier)]; !ok {
			t.Errorf("counts missing tier %q", tier)
		}
	}
	if got.Counts["none"] != 2 || got.Counts["has"] != 1 ||
		got.Counts["social"] != 1 || got.Counts["builder"] != 1 ||
		got.Counts["directory"] != 0 {
		t.Errorf("unexpected counts: %#v", got.Counts)
	}
	// no filter => total is all rows, page is all ids in order
	if got.Total != 5 {
		t.Errorf("Total = %d, want 5", got.Total)
	}
	wantIDs := []int{1, 2, 3, 4, 5}
	assertIDs(t, got.PageIDs, wantIDs)
}

func TestPaginate_FilterNoWebsiteTiers(t *testing.T) {
	// "no real website" = none+social+builder(+directory) => ids 1,3,4,5
	got := Paginate(rows(), []string{"none", "social", "directory", "builder"}, 50, 0)
	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	assertIDs(t, got.PageIDs, []int{1, 3, 4, 5})
	// counts are always the unfiltered breakdown
	if got.Counts["has"] != 1 {
		t.Errorf("counts should be unfiltered; has = %d, want 1", got.Counts["has"])
	}
}

func TestPaginate_OffsetAndLimit(t *testing.T) {
	got := Paginate(rows(), []string{"none", "social", "directory", "builder"}, 2, 2)
	// filtered ids are [1,3,4,5]; offset 2 limit 2 => [4,5]
	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	assertIDs(t, got.PageIDs, []int{4, 5})
}

func TestPaginate_OffsetBeyondEnd(t *testing.T) {
	got := Paginate(rows(), nil, 50, 999)
	if len(got.PageIDs) != 0 {
		t.Errorf("PageIDs = %v, want empty", got.PageIDs)
	}
	if got.Total != 5 {
		t.Errorf("Total = %d, want 5", got.Total)
	}
}

func assertIDs(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}
