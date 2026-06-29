// Package webpresence classifies a business's scraped website URL into a
// web-presence tier, used by the "businesses with no real website" lead filter.
//
// Classification depends only on the website string scraped from Google Maps.
// An empty string means Google has no website on record (the strongest
// "no website" signal). A non-empty string is matched against curated domain
// sets: social networks / link-in-bio pages, business directories, free
// website builders, and booking/ordering platforms are all treated as "not a
// real owned website" — the high-value segments for an agency pitch. Anything
// else is a real website.
package webpresence

import (
	"fmt"
	"net/url"
	"strings"
)

// Tier is the web-presence classification of a single business.
type Tier string

const (
	TierNone      Tier = "none"      // no website listed at all
	TierSocial    Tier = "social"    // only a social network / link-in-bio page
	TierDirectory Tier = "directory" // only a directory/aggregator listing
	TierBuilder   Tier = "builder"   // a free website-builder subdomain
	TierBooking   Tier = "booking"   // only a booking/ordering platform page
	TierHas       Tier = "has"       // a real, owned website
)

// AllTiers returns every valid tier in priority order (most pitchable first).
func AllTiers() []Tier {
	return []Tier{TierNone, TierSocial, TierDirectory, TierBuilder, TierBooking, TierHas}
}

// IsValidTier reports whether s is a recognized tier key.
func IsValidTier(s string) bool {
	switch Tier(s) {
	case TierNone, TierSocial, TierDirectory, TierBuilder, TierBooking, TierHas:
		return true
	default:
		return false
	}
}

// ParseTiers parses a comma-separated tier list (e.g. "none,social") into
// validated tier keys. Blank input returns (nil, nil) meaning "no filter".
// An unrecognized tier returns an error.
func ParseTiers(csv string) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, nil
	}
	var out []string
	for part := range strings.SplitSeq(csv, ",") {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		if !IsValidTier(t) {
			return nil, fmt.Errorf("invalid web_presence tier: %q", t)
		}
		out = append(out, t)
	}
	return out, nil
}

// Domain sets. A host matches a domain when it equals the domain or is a
// subdomain of it (e.g. "m.facebook.com" matches "facebook.com").
var (
	socialDomains = []string{
		"facebook.com", "fb.com", "fb.me",
		"instagram.com",
		"tiktok.com",
		"linkedin.com",
		"twitter.com", "x.com",
		"youtube.com", "youtu.be",
		"t.me",
		"wa.me", "whatsapp.com",
		"threads.net",
		"pinterest.com",
		"nextdoor.com",
		"vk.com",
		// link-in-bio
		"linktr.ee", "beacons.ai", "bio.link", "lnk.bio", "carrd.co",
		"taplink.cc", "campsite.bio", "msha.ke",
	}
	directoryDomains = []string{
		"yelp.com", "yellowpages.com", "bbb.org", "houzz.com",
		"angi.com", "angieslist.com", "thumbtack.com", "tripadvisor.com",
		"mapquest.com", "foursquare.com", "manta.com",
		"chamberofcommerce.com", "clutch.co", "g2.com",
	}
	builderDomains = []string{
		"business.site", "business.google.com", "sites.google.com",
		"wixsite.com", "weebly.com", "godaddysites.com",
		"wordpress.com", "blogspot.com", "jimdosite.com",
		"mystrikingly.com", "webnode.com",
	}
	// bookingDomains: third-party booking/ordering/scheduling platforms a
	// business "operates through" but does not own — for the agency pitch these
	// are effectively "no real website". Extend freely as new platforms appear.
	bookingDomains = []string{
		// food ordering / delivery
		"doordash.com", "ubereats.com", "grubhub.com", "seamless.com",
		"postmates.com", "chownow.com", "toasttab.com", "slicelife.com",
		// reservations
		"opentable.com", "resy.com",
		// appointments / beauty & wellness
		"booksy.com", "vagaro.com", "fresha.com", "treatwell.com",
		"mindbodyonline.com", "schedulicity.com", "setmore.com", "simplybook.me",
		// scheduling / payments-hosted ordering
		"calendly.com", "acuityscheduling.com", "squareup.com", "clover.com",
	}
)

// Classify returns the web-presence tier for a scraped website string.
func Classify(website string) Tier {
	host := normalizeHost(website)
	if host == "" {
		return TierNone
	}
	switch {
	case matchesAny(host, socialDomains):
		return TierSocial
	case matchesAny(host, directoryDomains):
		return TierDirectory
	case matchesAny(host, builderDomains):
		return TierBuilder
	case matchesAny(host, bookingDomains):
		return TierBooking
	default:
		return TierHas
	}
}

// normalizeHost extracts a lowercased, www/m-stripped host from a raw website
// string. It tolerates inputs without a scheme ("example.com/path") and returns
// "" for blank, "null", or unparseable input.
func normalizeHost(website string) string {
	s := strings.TrimSpace(strings.ToLower(website))
	if s == "" || s == "null" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	return host
}

// matchesAny reports whether host equals or is a subdomain of any domain in set.
func matchesAny(host string, set []string) bool {
	for _, d := range set {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
