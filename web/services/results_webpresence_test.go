package services

import (
	"testing"

	"github.com/gosom/google-maps-scraper/pkg/webpresence"
)

// Guards the contract the reworked GetEnhancedJobResultsPaginated relies on:
// the unfiltered counts cover every tier, and the "no website" filter selects
// none+social+directory+builder+booking while excluding real sites.
func TestResultsWebPresenceContract(t *testing.T) {
	rows := []webpresence.Row{
		{ID: 10, Website: ""},                                 // none
		{ID: 11, Website: "https://acme-roofing.com"},         // has
		{ID: 12, Website: "https://instagram.com/acme"},       // social
		{ID: 13, Website: "https://www.yelp.com/biz/x"},       // directory
		{ID: 14, Website: "https://acme.business.site"},       // builder
		{ID: 15, Website: "https://www.doordash.com/store/x"}, // booking
	}

	all := webpresence.Paginate(rows, nil, 50, 0)
	if all.Total != 6 || all.Counts["has"] != 1 || all.Counts["none"] != 1 {
		t.Fatalf("unfiltered: total=%d counts=%#v", all.Total, all.Counts)
	}

	noSite := webpresence.Paginate(rows, []string{"none", "social", "directory", "builder", "booking"}, 50, 0)
	if noSite.Total != 5 {
		t.Fatalf("no-website filter total = %d, want 5", noSite.Total)
	}
	for _, id := range noSite.PageIDs {
		if id == 11 {
			t.Fatalf("real-website row 11 leaked into no-website filter")
		}
	}
}
