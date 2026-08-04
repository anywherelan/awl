package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/entity"
)

// inviteNeverExpires is what --expires takes instead of a duration to make a
// link without an expiry, and what the list prints for one.
const inviteNeverExpires = "never"

// createInviteParams is the flags of `peers invite create`, with the Expires
// still in the human form the flag takes.
type createInviteParams struct {
	Uses                 int
	Expires              string
	Alias                string
	AllowUsingAsExitNode bool
	Label                string
	ShowQR               bool
}

func createInvite(api *apiclient.Client, params createInviteParams, w io.Writer) error {
	expiresInSeconds, err := parseInviteExpiry(params.Expires)
	if err != nil {
		return err
	}

	invite, err := api.CreateInvite(entity.CreateInviteRequest{
		MaxUses:              params.Uses,
		ExpiresInSeconds:     expiresInSeconds,
		Alias:                params.Alias,
		AllowUsingAsExitNode: params.AllowUsingAsExitNode,
		Label:                params.Label,
	})
	if err != nil {
		return err
	}

	// The link goes first and alone on its line, so that it can be picked out of the output by eye or by a script.
	fmt.Fprintln(w, invite.Link)
	fmt.Fprintln(w, formatInviteSummary(*invite))

	if params.ShowQR {
		qrterminal.GenerateHalfBlock(invite.Link, qrterminal.M, w)
	}

	return nil
}

// printInvites lists the invites. Links are printed only on demand.
func printInvites(api *apiclient.Client, showLinks bool, w io.Writer) error {
	invites, err := api.Invites()
	if err != nil {
		return err
	}
	if len(invites) == 0 {
		fmt.Fprintln(w, "you have no invite links")
		return nil
	}

	headers := []string{"id", "label", "uses", "expires", "status", "grants"}
	if showLinks {
		headers = append(headers, "link")
	}

	table := tablewriter.NewWriter(w)
	table.SetAutoWrapText(false)
	table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
	table.SetHeader(headers)

	for _, invite := range invites {
		row := []string{
			invite.ID,
			invite.Label,
			formatInviteUses(invite),
			formatInviteExpiry(invite.ExpiresAt),
			invite.Status,
			formatInviteGrants(invite),
		}
		if showLinks {
			row = append(row, invite.Link)
		}
		table.Append(row)
	}
	table.Render()

	return nil
}

// revokeInvite closes an invite by ID.
func revokeInvite(api *apiclient.Client, id string, w io.Writer) error {
	// IDs are lowercase hex, and this one was most likely retyped off a screen.
	err := api.RevokeInvite(strings.ToLower(strings.TrimSpace(id)))
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "invite revoked: it accepts no new peers, peers already added through it stay")
	return nil
}

// formatInviteSummary describes an invite in one line: what it costs to use it
// up, and what it grants to whoever does.
func formatInviteSummary(invite entity.InviteResponse) string {
	parts := []string{
		"id " + invite.ID,
		"uses " + formatInviteUses(invite),
		"expires " + formatInviteExpiry(invite.ExpiresAt),
	}
	if invite.Alias != "" {
		parts = append(parts, fmt.Sprintf("alias %q", invite.Alias))
	}
	if invite.AllowUsingAsExitNode {
		parts = append(parts, "may use you as exit node")
	}

	return strings.Join(parts, " | ")
}

func formatInviteUses(invite entity.InviteResponse) string {
	return fmt.Sprintf("%d/%d", invite.UsedCount, invite.MaxUses)
}

// formatInviteGrants renders, for the list, what a link decides in advance for whoever redeems it.
func formatInviteGrants(invite entity.InviteResponse) string {
	var parts []string
	if invite.AllowUsingAsExitNode {
		parts = append(parts, "exit node")
	}
	if invite.Alias != "" {
		parts = append(parts, fmt.Sprintf("alias %q", invite.Alias))
	}

	return strings.Join(parts, ", ")
}

// formatInviteExpiry renders the expiry as a date plus the time left.
func formatInviteExpiry(expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return inviteNeverExpires
	}

	date := expiresAt.Local().Format("2006-01-02 15:04")
	left := time.Until(expiresAt)
	if left <= 0 {
		return date + " (expired)"
	}

	return fmt.Sprintf("%s (in %s)", date, formatInviteTimeLeft(left))
}

// formatInviteTimeLeft renders how long a link still has in its largest unit
// alone, rounded to the nearest one: "24h", "6d". Rounded, not truncated, so
// that a link just made with --expires 24h does not greet its author with
// "23h". Not formatUptime, which is built for an uptime and counts down to the
// second — nobody reads an invite that closely, and the seconds would only
// churn in the table.
func formatInviteTimeLeft(left time.Duration) string {
	const day = 24 * time.Hour

	switch {
	case left >= day:
		return fmt.Sprintf("%dd", left.Round(day)/day)
	case left >= time.Hour:
		// Rounding can carry the value into the next unit, and the unit it lands
		// in is the one to say it in: "1d", never "24h".
		if left.Round(time.Hour) >= day {
			return "1d"
		}
		return fmt.Sprintf("%dh", left.Round(time.Hour)/time.Hour)
	case left >= time.Minute:
		if left.Round(time.Minute) >= time.Hour {
			return "1h"
		}
		return fmt.Sprintf("%dm", left.Round(time.Minute)/time.Minute)
	}

	return "<1m"
}

// parseInviteExpiry turns the --expires flag into the seconds the API takes.
func parseInviteExpiry(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, inviteNeverExpires) {
		return 0, nil
	}

	duration, err := parseDurationWithDays(value)
	if err != nil {
		return 0, fmt.Errorf("invalid expires value '%s': use a duration such as 30m, 24h, 7d, 1d6h, or '%s'", value, inviteNeverExpires)
	}
	if duration < time.Second {
		return 0, fmt.Errorf("expires must be at least 1s, or '%s' for a link that never expires", inviteNeverExpires)
	}

	return int64(duration.Seconds()), nil
}

// parseDurationWithDays is time.ParseDuration with one unit added: "d", a day
// of 24h, which the standard parser has no unit above an hour for — and a link
// that lives a week is an ordinary thing to ask for.
//
// Lifted from Caddy's caddy.ParseDuration (caddyserver/caddy, caddy.go), minus
// its length guard on the input, which here is a local flag. Every "<n>d" is
// rewritten into the equal number of hours and the standard parser does the
// rest, so the other units, the fractions and the error messages are all the
// ones people already know. It composes because time.ParseDuration sums
// repeated units: "1d6h" becomes "24h6h", which is 30h.
func parseDurationWithDays(s string) (time.Duration, error) {
	var inNumber bool
	var numStart int
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == 'd' {
			days, err := strconv.ParseFloat(s[numStart:i], 64)
			if err != nil {
				return 0, err
			}
			hours := strconv.FormatFloat(days*24, 'f', -1, 64)
			s = s[:numStart] + hours + "h" + s[i+1:]
			i--
			continue
		}
		if !inNumber {
			numStart = i
		}
		inNumber = (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' || ch == '+'
	}

	return time.ParseDuration(s)
}
