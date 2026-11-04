//go:build ee

package ee

import (
	"errors"
	"log/slog"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The ownership part of the GDPR export: offers and invitations the account
// sent and received, the sites it is an admin of, and the admins of its own
// sites.
//
// The rule for every address: show another party's email ONLY where the
// account already knew it. That is the address it typed itself (to_email of
// its own offers and invitations), the sender of something it was told about
// (from_email of a received offer it was notified of, or accepted; of a
// received invitation), the owner who invited it (owner_email of a share it
// accepted an invitation for), and the address of an admin of its own sites (it invited them). The
// account that accepted an offer is never named to the sender.

type gdprOfferSent struct {
	ID            string     `json:"id"`
	Sites         []string   `json:"sites"`
	ToEmail       string     `json:"to_email"`
	AvailableFrom *time.Time `json:"available_from,omitempty"`
	AcceptBy      time.Time  `json:"accept_by"`
	Created       time.Time  `json:"created"`
	State         string     `json:"state"`
}

type gdprOfferReceived struct {
	ID            string     `json:"id"`
	Sites         []string   `json:"sites"`
	FromEmail     string     `json:"from_email,omitempty"`
	AvailableFrom *time.Time `json:"available_from,omitempty"`
	AcceptBy      time.Time  `json:"accept_by"`
	Created       time.Time  `json:"created"`
	State         string     `json:"state"`
}

type gdprInviteSent struct {
	ID      string     `json:"id"`
	Site    string     `json:"site"`
	ToEmail string     `json:"to_email"`
	Until   *time.Time `json:"until,omitempty"`
	Created time.Time  `json:"created"`
	State   string     `json:"state"`
}

type gdprInviteReceived struct {
	ID        string     `json:"id"`
	Site      string     `json:"site"`
	FromEmail string     `json:"from_email,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	Created   time.Time  `json:"created"`
	State     string     `json:"state"`
}

// gdprSharedWithMe is a site the account is listed as an admin of.
type gdprSharedWithMe struct {
	Site       string     `json:"site"`
	Until      *time.Time `json:"until,omitempty"`
	OwnerEmail string     `json:"owner_email,omitempty"`
}

// gdprAdminOfMine is an admin of one of the account's own sites.
type gdprAdminOfMine struct {
	Site  string     `json:"site"`
	Email string     `json:"email,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

type gdprOwnership struct {
	OffersSent          []gdprOfferSent      `json:"offers_sent"`
	OffersReceived      []gdprOfferReceived  `json:"offers_received"`
	InvitationsSent     []gdprInviteSent     `json:"invitations_sent"`
	InvitationsReceived []gdprInviteReceived `json:"invitations_received"`
	SharedWithMe        []gdprSharedWithMe   `json:"shared_with_me"`
	AdminsOfMySites     []gdprAdminOfMine    `json:"admins_of_my_sites"`
}

// gdprOwnership builds the part. Whatever cannot be read is logged and left
// out: the rest of the export is still owed.
func (p *provider) gdprOwnership(acc *account.Account, ownedIDs []string) *gdprOwnership {
	out := &gdprOwnership{
		OffersSent: []gdprOfferSent{}, OffersReceived: []gdprOfferReceived{},
		InvitationsSent: []gdprInviteSent{}, InvitationsReceived: []gdprInviteReceived{},
		SharedWithMe: []gdprSharedWithMe{}, AdminsOfMySites: []gdprAdminOfMine{},
	}
	if p.owner == nil {
		return out
	}
	fail := func(what string, err error) {
		slog.Error("gdpr: could not read "+what+" for an export", "account", acc.ID, "err", err)
	}

	if os, err := p.owner.OffersBySender(acc.ID); err != nil {
		fail("the account's offers", err)
	} else {
		for _, o := range os {
			out.OffersSent = append(out.OffersSent, gdprOfferSent{
				ID: o.ID, Sites: o.Sites, ToEmail: o.ToEmail, AvailableFrom: o.AvailableFrom,
				AcceptBy: o.AcceptBy, Created: o.Created, State: o.State,
			})
		}
	}
	if acc.Email != "" {
		if os, err := p.owner.OffersByEmail(acc.Email); err != nil {
			fail("the offers made to the account", err)
		} else {
			for _, o := range os {
				r := gdprOfferReceived{
					ID: o.ID, Sites: o.Sites, AvailableFrom: o.AvailableFrom,
					AcceptBy: o.AcceptBy, Created: o.Created, State: o.State,
				}
				// The sender is known to the account only if the offer
				// reached it: it was mailed, or the account accepted it.
				if o.NotifiedAt != nil || o.AcceptedBy == acc.ID {
					r.FromEmail = p.emailOf(o.From)
				}
				out.OffersReceived = append(out.OffersReceived, r)
			}
		}
	}

	if is, err := p.owner.InvitesBySender(acc.ID); err != nil {
		fail("the account's invitations", err)
	} else {
		for _, i := range is {
			out.InvitationsSent = append(out.InvitationsSent, gdprInviteSent{
				ID: i.ID, Site: i.Site, ToEmail: i.ToEmail, Until: i.Until, Created: i.Created, State: i.State,
			})
		}
	}
	var invRecv []ownership.Invite
	if acc.Email != "" {
		if is, err := p.owner.InvitesByEmail(acc.Email); err != nil {
			fail("the invitations made to the account", err)
		} else {
			invRecv = is
			for _, i := range is {
				out.InvitationsReceived = append(out.InvitationsReceived, gdprInviteReceived{
					ID: i.ID, Site: i.Site, FromEmail: p.emailOf(i.From), Until: i.Until, Created: i.Created, State: i.State,
				})
			}
		}
	}

	// Shares: the sites the account is an admin of, by meta.json.
	if marked, err := p.owner.SharedSites(acc.ID); err != nil {
		fail("the account's shares", err)
	} else {
		for _, id := range marked {
			admins, err := p.siteAdmins(id)
			if err != nil {
				if !errors.Is(err, ext.ErrSiteGone) {
					fail("the admins of a shared site", err)
				}
				continue
			}
			for _, a := range admins {
				if a.Account != acc.ID {
					continue
				}
				s := gdprSharedWithMe{Site: id, Until: a.Until}
				// The owner's address is known if the owner invited the
				// account to this site and still owns it.
				if ownerID, err := p.siteOwner(id); err == nil && ownerID != "" {
					for _, i := range invRecv {
						if i.Site == id && i.From == ownerID && i.State == ownership.StateAccepted {
							s.OwnerEmail = p.emailOf(ownerID)
							break
						}
					}
				}
				out.SharedWithMe = append(out.SharedWithMe, s)
			}
		}
	}

	// The admins of the account's own sites: the owner invited each of them
	// by that address and sees it on its dashboard.
	for _, id := range ownedIDs {
		admins, err := p.siteAdmins(id)
		if err != nil {
			if !errors.Is(err, ext.ErrSiteGone) {
				fail("the admins of an owned site", err)
			}
			continue
		}
		for _, a := range admins {
			row := gdprAdminOfMine{Site: id, Until: a.Until}
			if adm, err := p.accounts.ByID(a.Account); err == nil {
				row.Email = adm.Email
			}
			out.AdminsOfMySites = append(out.AdminsOfMySites, row)
		}
	}
	return out
}

// ---- the operator's register ----

// openOffersBySite groups the offers that are not final (open, paused,
// accepting) by site, for the register. A read error is logged and shows none.
func (p *provider) openOffersBySite() map[string][]ownership.Offer {
	out := map[string][]ownership.Offer{}
	if p.owner == nil {
		return out
	}
	offers, err := p.owner.OpenOffers()
	if err != nil {
		slog.Error("admin: could not read the open offers", "err", err)
		return out
	}
	for _, o := range offers {
		for _, id := range o.Sites {
			out[id] = append(out[id], o)
		}
	}
	return out
}

// shareLines is how the register states a site's admins (email, until) and
// its open, paused or accepting offers (to-email, dates, state). The operator
// sees the addresses: this is the operator's console.
func (p *provider) shareLines(admins []ext.SiteAdmin, offers []ownership.Offer) []string {
	var lines []string
	for _, a := range admins {
		who := a.Account
		if acc, err := p.accounts.ByID(a.Account); err == nil {
			who = acc.Email
		}
		line := "admin " + who
		if a.Until != nil {
			line += " until " + a.Until.Local().Format("2006-01-02")
		}
		lines = append(lines, line)
	}
	for _, o := range offers {
		line := "offer to " + o.ToEmail + " · " + o.State
		if o.AvailableFrom != nil {
			line += " · from " + o.AvailableFrom.Local().Format("2006-01-02")
		}
		line += " · accept by " + o.AcceptBy.Local().Format("2006-01-02") + " · made " + o.Created.Local().Format("2006-01-02")
		lines = append(lines, line)
	}
	return lines
}

// registerOffersOf is the offers of one site that are not final.
func (p *provider) registerOffersOf(viewID string) []ownership.Offer {
	if p.owner == nil {
		return nil
	}
	all, err := p.owner.OffersBySite(viewID)
	if err != nil {
		slog.Error("admin: could not read a site's offers", "site", viewID, "err", err)
		return nil
	}
	var out []ownership.Offer
	for _, o := range all {
		if !ownership.IsFinal(o.State) {
			out = append(out, o)
		}
	}
	return out
}
