package analytics

import "testing"

func TestChannel(t *testing.T) {
	q := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		name string
		r    Record
		want string
	}{
		{"gclid", Record{Ck: []string{"gclid"}}, "Paid search"},
		{"msclkid with referrer", Record{Ck: []string{"msclkid"}, R: "www.bing.com/"}, "Paid search"},
		{"cpc google", Record{Q: q("utm_medium", "cpc", "utm_source", "google")}, "Paid search"},
		{"cpc case", Record{Q: q("utm_medium", "CPC", "utm_source", "Google")}, "Paid search"},
		{"paid-social", Record{Q: q("utm_source", "meta", "utm_medium", "paid-social")}, "Paid social"},
		{"cpc facebook", Record{Q: q("utm_source", "facebook", "utm_medium", "cpc")}, "Paid social"},
		{"fbclid paid", Record{Ck: []string{"fbclid"}, Q: q("utm_medium", "paid")}, "Paid social"},
		{"fbclid social referrer", Record{Ck: []string{"fbclid"}, R: "l.facebook.com/"}, "Organic social"},
		{"fbclid alone", Record{Ck: []string{"fbclid"}}, "Direct"},
		{"fbclid other referrer", Record{Ck: []string{"fbclid"}, R: "example.org/"}, "Referral"},
		{"newsletter", Record{Q: q("utm_medium", "newsletter")}, "Email"},
		{"mail host", Record{R: "mail.google.com/"}, "Email"},
		{"google referrer", Record{R: "www.google.com/"}, "Organic search"},
		{"google at", Record{R: "google.at/search"}, "Organic search"},
		{"duckduckgo", Record{R: "duckduckgo.com/"}, "Organic search"},
		{"facebook referrer", Record{R: "l.facebook.com/"}, "Organic social"},
		{"t.co", Record{R: "t.co/abc"}, "Organic social"},
		{"linkedin social", Record{Q: q("utm_source", "linkedin", "utm_medium", "social")}, "Organic social"},
		{"other campaign", Record{Q: q("utm_source", "x")}, "Other campaign"},
		{"referral", Record{R: "example.org/"}, "Referral"},
		{"not google", Record{R: "notgoogle.com/"}, "Referral"},
		{"direct", Record{}, "Direct"},
	}
	for _, c := range cases {
		if got := Channel(c.r); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
