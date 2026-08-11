package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

const (
	// inviteIDBytes is the entropy of the invite ID, hex-encoded into
	// 2*inviteIDBytes characters. It only has to be unique among our own
	// invites, not unguessable — the token is the secret — and short enough to
	// retype into `awl cli peers invite revoke --id`. Collisions are retried,
	// so this can be raised without touching anything else: no caller assumes a
	// length.
	inviteIDBytes = 2
	// inviteTokenBytes is the entropy of the bearer token. 128 bits makes
	// brute force pointless (every attempt costs a libp2p connection)
	inviteTokenBytes = 16
)

// Reasons an invite cannot be redeemed. They double as the metric label in
// service.AuthStatus, so they are distinguished rather than folded into one.
var (
	ErrInviteNotFound = errors.New("invite not found")
	ErrInviteRevoked  = errors.New("invite is revoked")
	ErrInviteExpired  = errors.New("invite has expired")
	ErrInviteUsedUp   = errors.New("invite has no uses left")
)

// CreateInviteParams are the caller-chosen fields of a new invite; ID, Token
// and CreatedAt are generated.
type CreateInviteParams struct {
	Label string
	// Alias for the peer that redeems the invite. Only meaningful for
	// single-use invites — aliases must be unique.
	Alias                string
	AllowUsingAsExitNode bool
	// MaxUses below 1 is clamped to 1; the API layer bounds the upper end.
	MaxUses int
	// ExpiresAt zero means the invite never expires.
	ExpiresAt time.Time
}

func (c *Config) CreateInvite(params CreateInviteParams) (Invite, error) {
	token, err := generateInviteToken()
	if err != nil {
		return Invite{}, err
	}

	c.Lock()
	defer c.Unlock()

	id, err := c.generateInviteIDUnlocked()
	if err != nil {
		return Invite{}, err
	}

	// An invite with no uses would be born spent and could never be redeemed,
	// so a caller that forgot to set MaxUses gets the single-use default rather
	// than a dead link.
	if params.MaxUses < 1 {
		params.MaxUses = 1
	}

	invite := Invite{
		ID:                     id,
		Token:                  token,
		Label:                  params.Label,
		Alias:                  params.Alias,
		WeAllowUsingAsExitNode: params.AllowUsingAsExitNode,
		MaxUses:                params.MaxUses,
		ExpiresAt:              params.ExpiresAt,
		CreatedAt:              time.Now(),
	}
	c.Invites[id] = invite
	c.Save()

	return invite, nil
}

// ListInvites returns a copy of all invites, spent ones included.
func (c *Config) ListInvites() []Invite {
	c.RLock()
	defer c.RUnlock()

	invites := make([]Invite, 0, len(c.Invites))
	for _, invite := range c.Invites {
		invites = append(invites, invite)
	}

	sort.SliceStable(invites, func(i, j int) bool {
		return invites[i].CreatedAt.After(invites[j].CreatedAt)
	})

	return invites
}

func (c *Config) GetInvite(id string) (Invite, bool) {
	c.RLock()
	defer c.RUnlock()

	invite, ok := c.Invites[id]
	return invite, ok
}

// RevokeInvite closes an invite to new connections. Peers already added through
// it keep working; revoking is not kicking anyone out.
func (c *Config) RevokeInvite(id string) bool {
	c.Lock()
	defer c.Unlock()

	invite, ok := c.Invites[id]
	if !ok {
		return false
	}
	if !invite.Revoked {
		invite.Revoked = true
		c.Invites[id] = invite
		c.Save()
	}
	return true
}

// CheckInvite reports whether token matches an invite that can be redeemed
// right now, consuming nothing. It is a peek, used to decide what to do with an
// auth request (and to label the rejection metric); the authoritative check is
// ReserveInviteUnlocked.
func (c *Config) CheckInvite(token string) (Invite, error) {
	c.RLock()
	defer c.RUnlock()

	return c.findUsableInviteUnlocked(token, time.Now())
}

// ReserveInviteUnlocked consumes one use of the invite matching token. The
// caller must hold the write lock, and must perform the write that redeems the
// invite — inserting the peer, confirming it — inside the same critical
// section.
func (c *Config) ReserveInviteUnlocked(token string) (Invite, error) {
	invite, err := c.findUsableInviteUnlocked(token, time.Now())
	if err != nil {
		return invite, err
	}

	invite.UsedCount++
	c.Invites[invite.ID] = invite
	c.Save()

	return invite, nil
}

// findUsableInviteUnlocked looks an invite up by its token. Invites number in
// the tens, so a scan is enough and no token index has to be kept in sync.
func (c *Config) findUsableInviteUnlocked(token string, now time.Time) (Invite, error) {
	if token == "" {
		return Invite{}, ErrInviteNotFound
	}

	for _, invite := range c.Invites {
		if invite.Token != token {
			continue
		}
		if err := invite.usable(now); err != nil {
			return invite, err
		}
		return invite, nil
	}

	return Invite{}, ErrInviteNotFound
}

// IsExpired reports whether the invite is past its expiry by our own clock.
// Clock skew between nodes plays no part: only the creator checks this.
func (i Invite) IsExpired(now time.Time) bool {
	return !i.ExpiresAt.IsZero() && now.After(i.ExpiresAt)
}

func (i Invite) IsUsedUp() bool {
	return i.UsedCount >= i.MaxUses
}

// usable returns nil if the invite can be redeemed at now, otherwise the reason
// it cannot.
func (i Invite) usable(now time.Time) error {
	switch {
	case i.Revoked:
		return ErrInviteRevoked
	case i.IsExpired(now):
		return ErrInviteExpired
	case i.IsUsedUp():
		return ErrInviteUsedUp
	}
	return nil
}

func generateInviteToken() (string, error) {
	buf := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// generateInviteIDUnlocked returns an ID not yet present in Invites; the caller must hold the lock.
func (c *Config) generateInviteIDUnlocked() (string, error) {
	buf := make([]byte, inviteIDBytes)
	for range 10 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		id := hex.EncodeToString(buf)
		if _, exists := c.Invites[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("could not generate a unique invite id")
}
