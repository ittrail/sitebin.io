//go:build ee

package ee

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/containers"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// checkLine is one line of a limits check: a fact about whether a set of
// sites fits an account's CURRENT plan.
type checkLine struct {
	// OK is false when the line is a reason to refuse.
	OK bool
	// Label names what was measured: "Sharing", "Sites", "Container
	// services", "Storage", "Files", "Custom domains" or "Forms".
	Label string
	// Detail is the sentence for the person: what the account has, what the
	// sites add and what the plan allows.
	Detail string
	// Site names the site a per-site line is about (its name, else its
	// address, else its id); empty for a line about the whole set.
	Site string
}

// failedLines is the lines that are reasons to refuse.
func failedLines(lines []checkLine) []checkLine {
	var out []checkLine
	for _, l := range lines {
		if !l.OK {
			out = append(out, l)
		}
	}
	return out
}

// Narrow seams over the core's ext.OwnershipSites, asserted one by one like
// siteOwnerReader so a SiteService that lacks one stays whole.
type siteSizer interface {
	SiteBytes(viewID string) (bytes int64, files int, err error)
}

type moveReporter interface {
	WillNotMove(viewID, sender string) []string
}

// limitsCheck asks whether the sites in viewIDs, taken into acc, fit the
// account's CURRENT plan: that its plan shares at all, that the sites fit its
// site cap together, that their running container services fit the account's
// container cap together with what it already runs, and that each site fits
// the plan's per-site caps (storage, files, custom domains that would move,
// forms). It is built for a bundle: adoption passes one site, a transfer
// accept passes the whole offer.
//
// It stays on the safe side of every unknown. A lookup that fails (the plan,
// the owned list, a site, its size, the container listing) is returned as an
// error, never as a passing line, and the caller refuses. Where a figure
// cannot matter (no running services to add) it is not asked for.
//
// Without tiers (accounts mode) there are no plan caps to measure, so only
// the sharing line is returned; the instance-wide caps still apply when the
// site is written to.
func (p *provider) limitsCheck(acc *account.Account, viewIDs []string) ([]checkLine, error) {
	return p.limitsCheckWith(acc, viewIDs, nil)
}

// limitsCheckWith is limitsCheck with the sites' Info already read (infos,
// by view id; a site missing from it is read here). Info walks a site's
// files, so a caller that has it, like the transfer card, passes it on.
func (p *provider) limitsCheckWith(acc *account.Account, viewIDs []string, infos map[string]ext.SiteInfo) ([]checkLine, error) {
	allowed, err := p.SharingAllowed(acc.ID)
	if err != nil {
		return nil, fmt.Errorf("could not read the plan: %w", err)
	}
	lines := []checkLine{{
		OK: allowed, Label: "Sharing",
		Detail: map[bool]string{true: "Your plan includes taking sites over.", false: "Your plan does not include taking sites over."}[allowed],
	}}
	if p.cfg.Mode != eeconfig.ModeTiers {
		return lines, nil
	}
	tier, err := p.effectiveTierStrict(acc)
	if err != nil {
		return nil, fmt.Errorf("could not read the plan: %w", err)
	}

	read := make([]ext.SiteInfo, 0, len(viewIDs))
	for _, id := range viewIDs {
		info, ok := infos[id]
		if !ok {
			if info, ok = p.host.Sites().Info(id); !ok {
				return nil, fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
			}
		}
		read = append(read, info)
	}

	// Sites: counted by meta.json's owner, not the markers (ownedSiteIDs).
	if tier.MaxSites > 0 {
		owned, err := p.ownedSiteIDs(acc)
		if err != nil {
			return nil, err
		}
		n := len(owned) + len(viewIDs)
		lines = append(lines, checkLine{
			OK: n <= tier.MaxSites, Label: "Sites",
			Detail: fmt.Sprintf("You own %d, these add %d, and your plan allows %d.", len(owned), len(viewIDs), tier.MaxSites),
		})
	}

	// Container services: the account's running services plus these sites'.
	incoming := 0
	for _, id := range viewIDs {
		cs, err := p.host.Sites().ContainerSite(id)
		if err != nil {
			return nil, err
		}
		if containers.Counts(cs) {
			incoming += len(cs.Observed.Services)
		}
	}
	if incoming > 0 {
		running := 0
		if p.servicesOf != nil {
			if running, err = p.servicesOf(acc.ID); err != nil {
				return nil, fmt.Errorf("could not count the services you run: %w", err)
			}
		}
		lines = append(lines, checkLine{
			OK: running+incoming <= tier.MaxContainers, Label: "Container services",
			Detail: fmt.Sprintf("You run %d, these add %d, and your plan allows %d.", running, incoming, tier.MaxContainers),
		})
	}

	sizer, canSize := p.host.Sites().(siteSizer)
	if !canSize {
		return nil, errors.New("the site service cannot measure a site")
	}
	mover, _ := p.host.Sites().(moveReporter)
	for i, id := range viewIDs {
		info := read[i]
		name := siteLabel(info)
		bytes, files, err := sizer.SiteBytes(id)
		if err != nil {
			return nil, fmt.Errorf("could not measure %s: %w", name, err)
		}
		if tier.MaxSiteBytes > 0 {
			lines = append(lines, checkLine{
				OK: bytes <= tier.MaxSiteBytes, Label: "Storage", Site: name,
				Detail: fmt.Sprintf("%s used, and your plan allows %s per site.", humanBytes(bytes), humanBytes(tier.MaxSiteBytes)),
			})
		}
		if tier.MaxFiles > 0 {
			lines = append(lines, checkLine{
				OK: files <= tier.MaxFiles, Label: "Files", Site: name,
				Detail: fmt.Sprintf("%d files, and your plan allows %d per site.", files, tier.MaxFiles),
			})
		}
		// A custom domain counts when it moves with the site. A zone name,
		// an operator-zone name or one held through its host is detached by a
		// transfer and does not.
		var stays []string
		if mover != nil {
			stays = mover.WillNotMove(id, info.Owner)
		}
		moving := 0
		for _, d := range info.Domains {
			if !slices.Contains(stays, d) {
				moving++
			}
		}
		lines = append(lines, checkLine{
			OK: moving <= tier.CustomDomains, Label: "Custom domains", Site: name,
			Detail: fmt.Sprintf("%d custom domain(s) would move with it, and your plan allows %d per site.", moving, tier.CustomDomains),
		})
		lines = append(lines, checkLine{
			OK: info.Forms <= tier.MaxForms, Label: "Forms", Site: name,
			Detail: fmt.Sprintf("%d form(s), and your plan allows %d per site.", info.Forms, tier.MaxForms),
		})
	}
	return lines, nil
}

// siteLabel is how a line names a site: the owner's name for it, else its
// address, else its id.
func siteLabel(info ext.SiteInfo) string {
	switch {
	case info.Name != "" && info.ViewURL != "":
		return info.Name + " (" + info.ViewURL + ")"
	case info.Name != "":
		return info.Name
	case info.ViewURL != "":
		return info.ViewURL
	}
	return info.ViewID
}
