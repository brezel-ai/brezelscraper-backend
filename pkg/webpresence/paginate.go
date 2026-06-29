package webpresence

// Row is a minimal result row used to classify and paginate before fetching
// full-fat data. Website is the raw scraped website string.
type Row struct {
	ID      int
	Website string
}

// PageResult is the outcome of Paginate.
type PageResult struct {
	// PageIDs are the result IDs for the requested page, in display order,
	// after the tier filter is applied.
	PageIDs []int
	// Total is the number of rows matching the tier filter (all rows when the
	// filter is empty). Used for pagination/has_more.
	Total int
	// Counts is the UNFILTERED per-tier breakdown for the whole job, zero-filled
	// for every tier. Used for the "X of Y" UI summary.
	Counts map[string]int
}

// Paginate classifies each row, builds the unfiltered per-tier counts, applies
// the tier filter (empty/nil tiers => no filter), and slices the page window.
// Input rows must already be in the desired display order.
func Paginate(rows []Row, tiers []string, limit, offset int) PageResult {
	allowed := map[string]bool{}
	for _, t := range tiers {
		allowed[t] = true
	}
	filterOn := len(allowed) > 0

	counts := make(map[string]int, len(AllTiers()))
	for _, t := range AllTiers() {
		counts[string(t)] = 0
	}

	var filtered []int
	for _, r := range rows {
		tier := string(Classify(r.Website))
		counts[tier]++
		if !filterOn || allowed[tier] {
			filtered = append(filtered, r.ID)
		}
	}

	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit < 0 || end > total {
		end = total
	}
	page := append([]int(nil), filtered[offset:end]...)

	return PageResult{PageIDs: page, Total: total, Counts: counts}
}
