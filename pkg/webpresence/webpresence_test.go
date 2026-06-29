package webpresence

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		website string
		want    Tier
	}{
		{"empty", "", TierNone},
		{"whitespace", "   ", TierNone},
		{"literal null", "null", TierNone},
		{"real site", "https://www.acme-roofing.com", TierHas},
		{"real site no scheme", "acme-roofing.com/contact", TierHas},
		{"facebook", "https://www.facebook.com/acmeroofing", TierSocial},
		{"facebook mobile", "https://m.facebook.com/acmeroofing", TierSocial},
		{"instagram", "http://instagram.com/acme", TierSocial},
		{"x dot com", "https://x.com/acme", TierSocial},
		{"linktree", "https://linktr.ee/acme", TierSocial},
		{"whatsapp", "https://wa.me/15551234567", TierSocial},
		{"yelp directory", "https://www.yelp.com/biz/acme-roofing", TierDirectory},
		{"google business builder", "https://acme.business.site", TierBuilder},
		{"wix builder", "https://acme.wixsite.com/roofing", TierBuilder},
		{"doordash booking", "https://www.doordash.com/store/acme-12345", TierBooking},
		{"opentable booking", "https://www.opentable.com/r/acme", TierBooking},
		{"booksy booking", "https://booksy.com/en-us/12345_acme", TierBooking},
		{"uppercase normalized", "HTTPS://WWW.FACEBOOK.COM/Acme", TierSocial},
		// guards against naive substring matching (e.g. "x.com" inside "netflix.com")
		{"not-x", "https://netflix.com", TierHas},
		{"subdomain-of-real", "https://blog.acme.com", TierHas},
		// guards against a real domain that merely contains a brand token
		{"facebook-lookalike", "https://notfacebook.com", TierHas},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.website); got != c.want {
				t.Errorf("Classify(%q) = %q, want %q", c.website, got, c.want)
			}
		})
	}
}

func TestIsValidTier(t *testing.T) {
	for _, ok := range []string{"none", "social", "directory", "builder", "booking", "has"} {
		if !IsValidTier(ok) {
			t.Errorf("IsValidTier(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "nope", "None", "website"} {
		if IsValidTier(bad) {
			t.Errorf("IsValidTier(%q) = true, want false", bad)
		}
	}
}

func TestParseTiers(t *testing.T) {
	got, err := ParseTiers("none, social ,builder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"none", "social", "builder"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	if _, err := ParseTiers("none,bogus"); err == nil {
		t.Error("expected error for invalid tier, got nil")
	}

	empty, err := ParseTiers("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if empty != nil {
		t.Errorf("ParseTiers(\"\") = %v, want nil", empty)
	}
}
