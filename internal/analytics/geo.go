package analytics

import (
	"net/netip"

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

// Attribution is the database's English description, "" if it has none.
func (g *Geo) Attribution() string {
	if g == nil || g.r == nil {
		return ""
	}
	d := g.r.Metadata.Description
	if v, ok := d["en"]; ok {
		return v
	}
	return d[""]
}
