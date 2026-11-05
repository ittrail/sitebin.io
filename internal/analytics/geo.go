package analytics

import (
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Geo looks up the country, region and city of an address in a MaxMind-format
// database. A nil *Geo is valid and finds nothing.
type Geo struct {
	r *maxminddb.Reader
}

// OpenGeo opens the database at path; an empty path gives nil, nil.
func OpenGeo(path string) (*Geo, error) {
	if path == "" {
		return nil, nil
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &Geo{r: r}, nil
}

type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Lookup returns the ISO country code, the region and the English city name.
// The region is the ISO 3166-2 code ("CC-XX") when the record has one,
// otherwise the subdivision's English name. Unknown addresses give "".
func (g *Geo) Lookup(ip string) (country, region, city string) {
	if g == nil || g.r == nil {
		return "", "", ""
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", ""
	}
	var rec geoRecord
	res := g.r.Lookup(addr.Unmap())
	if !res.Found() || res.Decode(&rec) != nil {
		return "", "", ""
	}
	country = rec.Country.ISOCode
	if len(rec.Subdivisions) > 0 {
		sd := rec.Subdivisions[0]
		switch {
		case sd.ISOCode != "" && country != "":
			region = country + "-" + sd.ISOCode
		case sd.ISOCode != "":
			region = sd.ISOCode
		default:
			region = sd.Names["en"]
		}
	}
	city = rec.City.Names["en"]
	return country, region, city
}

// Attribution is what the dashboard shows for the database: its text and,
// when the licence asks for one, a link. See attributionFor.
func (g *Geo) Attribution() (text, link string) {
	if g == nil || g.r == nil {
		return "", ""
	}
	return attributionFor(g.r.Metadata.DatabaseType, g.r.Metadata.Description)
}

// dbipAttribution is the attribution DB-IP's Lite databases ask for (CC BY
// 4.0: credit with a link). Their metadata describes them only as
// "DB-IP.com - IP to City", which is no attribution.
const (
	dbipAttribution = "IP geolocation by DB-IP"
	dbipLink        = "https://db-ip.com"
)

// attributionFor is the attribution of a database of type dbType with the
// descriptions desc (by language): DB-IP's own text and link when the type
// or the description names DB-IP, otherwise the English description (or
// the one without a language), with no link.
func attributionFor(dbType string, desc map[string]string) (text, link string) {
	en, ok := desc["en"]
	if !ok {
		en = desc[""]
	}
	if strings.Contains(strings.ToLower(dbType), "dbip") || strings.Contains(strings.ToLower(en), "db-ip") {
		return dbipAttribution, dbipLink
	}
	return en, ""
}
