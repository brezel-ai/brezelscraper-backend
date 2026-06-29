package webpresence

// Scrape-time website pre-filter values (sent by the new-job wizard).
const (
	FilterAll        = "all"
	FilterNoWebsite  = "no_website"
	FilterHasWebsite = "has_website"
)

// IsValidFilter reports whether s is a recognized scrape-time filter value.
// The empty string means "no filter" and is valid.
func IsValidFilter(s string) bool {
	switch s {
	case "", FilterAll, FilterNoWebsite, FilterHasWebsite:
		return true
	default:
		return false
	}
}

// FilterApplied reports whether the filter actually restricts results — used to
// decide whether to charge the "filters_applied" billing event.
func FilterApplied(filter string) bool {
	return filter == FilterNoWebsite || filter == FilterHasWebsite
}

// Keep reports whether a business with the given tier survives the filter.
// Empty/"all" keeps everything; "no_website" keeps everything except a real
// owned site; "has_website" keeps only real owned sites.
func Keep(filter string, tier Tier) bool {
	switch filter {
	case FilterNoWebsite:
		return tier != TierHas
	case FilterHasWebsite:
		return tier == TierHas
	default:
		return true
	}
}

// KeepWebsite classifies the raw website string, then applies Keep.
func KeepWebsite(filter, website string) bool {
	return Keep(filter, Classify(website))
}
