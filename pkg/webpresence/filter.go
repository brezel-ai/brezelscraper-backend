package webpresence

import (
	"fmt"
	"strings"
)

// Scrape-time website pre-filter values (sent by the new-job wizard).
const (
	FilterAll        = "all"
	FilterNoWebsite  = "no_website"
	FilterHasWebsite = "has_website"
)

// ParseQueryFilter parses the `web_presence` query parameter accepted by the
// results endpoint into a de-duplicated tier allow-list (the format the read
// model consumes). It accepts a comma-separated list whose tokens may be:
//
//   - a fine-grained tier:  none, social, directory, builder, booking, has
//   - a group alias:        all, no_website, has_website
//
// Group aliases expand to their member tiers, so the SAME vocabulary used to
// pre-filter a scrape (website_filter=no_website) also works when reading
// results back (web_presence=no_website). Tiers and aliases may be mixed; the
// result is their union in canonical tier order. Blank input, or the `all`
// alias, means "no filter" and returns (nil, nil). An unrecognized token
// returns an error naming the offending value.
//
// This is the public, consumer-facing parser. ParseTiers remains the strict
// tier-only primitive used internally.
func ParseQueryFilter(csv string) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, nil
	}

	seen := make(map[Tier]bool, len(AllTiers()))
	sawAll := false
	for part := range strings.SplitSeq(csv, ",") {
		tok := strings.ToLower(strings.TrimSpace(part))
		switch {
		case tok == "":
			continue
		case tok == FilterAll:
			sawAll = true
		case tok == FilterNoWebsite, tok == FilterHasWebsite:
			for _, t := range AllTiers() {
				if Keep(tok, t) {
					seen[t] = true
				}
			}
		case IsValidTier(tok):
			seen[Tier(tok)] = true
		default:
			return nil, fmt.Errorf(
				"invalid web_presence value: %q (expected a tier "+
					"none|social|directory|builder|booking|has or a group "+
					"all|no_website|has_website)", tok)
		}
	}

	// "all" (even mixed with other tokens) and a covers-everything selection
	// both mean no restriction; return nil so the read model skips filtering.
	if sawAll || len(seen) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(seen))
	for _, t := range AllTiers() { // canonical order, de-duplicated
		if seen[t] {
			out = append(out, string(t))
		}
	}
	return out, nil
}

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
