package analytics

import "strings"

// Host lists for Channel. Entries ending in "." match a label prefix
// ("google." matches www.google.com and google.at); other entries match the
// host itself or any subdomain ("t.co" matches t.co, never microsoft.co).
var (
	searchHosts = []string{
		"google.", "bing.com", "duckduckgo.com", "yahoo.", "ecosia.org",
		"qwant.com", "yandex.", "baidu.com", "startpage.com", "search.brave.com",
	}
	socialHosts = []string{
		"facebook.com", "fb.com", "instagram.com", "linkedin.com", "lnkd.in",
		"twitter.com", "x.com", "t.co", "youtube.com", "youtu.be",
		"pinterest.", "reddit.com", "tiktok.com", "whatsapp.com", "wa.me",
		"telegram.org", "t.me", "xing.com",
	}
	mailHosts = []string{
		"mail.google.com", "outlook.live.com", "outlook.office.com",
		"outlook.office365.com", "mail.yahoo.com", "mail.proton.me",
		"mail.protonmail.com", "webmail.", "mail.aol.com", "mail.ru",
	}
	// utm_source values naming a search engine or a social network.
	searchSources = []string{
		"google", "bing", "duckduckgo", "yahoo", "ecosia", "qwant", "yandex",
		"baidu", "startpage", "brave",
	}
	socialSources = []string{
		"facebook", "fb", "meta", "instagram", "ig", "linkedin", "twitter", "x",
		"t.co", "youtube", "pinterest", "reddit", "tiktok", "whatsapp",
		"telegram", "xing",
	}
	paidSearchClickIDs = []string{"gclid", "gbraid", "wbraid", "msclkid"}
	paidSocialClickIDs = []string{"fbclid", "ttclid", "li_fat_id", "twclid"}
	paidMediums        = []string{"cpc", "ppc", "paid", "paidsearch", "paid-social", "paidsocial", "social-paid"}
	paidSocialMediums  = []string{"paid-social", "paidsocial", "social-paid"}
	emailMediums       = []string{"email", "e-mail", "newsletter"}
)

// Channel classifies the entry record of a visit. Rules apply in this
// order: Paid search, Paid social, Email, Organic search, Organic social,
// Other campaign, Referral, Direct.
func Channel(entry Record) string {
	medium := strings.ToLower(entry.Q["utm_medium"])
	source := strings.ToLower(entry.Q["utm_source"])
	host := refHost(entry.R)

	paid := inList(medium, paidMediums)
	searchSrc := inList(source, searchSources)
	socialSrc := inList(source, socialSources)
	social := hostIn(host, socialHosts)

	switch {
	case hasAny(entry.Ck, paidSearchClickIDs), paid && !inList(medium, paidSocialMediums) && searchSrc:
		return "Paid search"
	case inList(medium, paidSocialMediums), paid && hasAny(entry.Ck, paidSocialClickIDs), paid && socialSrc:
		return "Paid social"
	case inList(medium, emailMediums), hostIn(host, mailHosts):
		return "Email"
	case hostIn(host, searchHosts):
		return "Organic search"
	case social, medium == "social":
		return "Organic social"
	}
	for k := range entry.Q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") {
			return "Other campaign"
		}
	}
	if host != "" {
		return "Referral"
	}
	return "Direct"
}

func refHost(r string) string {
	h, _, _ := strings.Cut(r, "/")
	return strings.ToLower(strings.TrimSpace(h))
}

func inList(v string, list []string) bool {
	if v == "" {
		return false
	}
	for _, s := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasAny(have, want []string) bool {
	for _, h := range have {
		if inList(strings.ToLower(h), want) {
			return true
		}
	}
	return false
}

func hostIn(host string, list []string) bool {
	if host == "" {
		return false
	}
	for _, e := range list {
		if strings.HasSuffix(e, ".") {
			if strings.HasPrefix(host, e) || strings.Contains(host, "."+e) {
				return true
			}
			continue
		}
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}
