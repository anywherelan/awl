package config

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateInvite(t *testing.T) {
	conf, _ := newTestConfig(t)

	invite, err := conf.CreateInvite(CreateInviteParams{
		Label:                "laptop",
		Alias:                "my-laptop",
		AllowUsingAsExitNode: true,
		MaxUses:              1,
	})
	require.NoError(t, err)

	assert.Len(t, invite.ID, hex.EncodedLen(inviteIDBytes))
	assert.NotEmpty(t, invite.Token)
	assert.NotEqual(t, invite.ID, invite.Token)
	assert.True(t, invite.ExpiresAt.IsZero())
	assert.False(t, invite.CreatedAt.IsZero())

	stored, ok := conf.GetInvite(invite.ID)
	require.True(t, ok)
	assert.Equal(t, invite, stored)

	second, err := conf.CreateInvite(CreateInviteParams{MaxUses: 1})
	require.NoError(t, err)
	assert.NotEqual(t, invite.ID, second.ID)
	assert.NotEqual(t, invite.Token, second.Token)
	assert.Len(t, conf.ListInvites(), 2)

	// A caller that forgets MaxUses must not get an invite that is born spent.
	withoutUses, err := conf.CreateInvite(CreateInviteParams{})
	require.NoError(t, err)
	assert.Equal(t, 1, withoutUses.MaxUses)
	assert.False(t, withoutUses.IsUsedUp())
}

// TestInviteJSONTags pins the on-disk names of the invite fields. They are a
// compatibility boundary: renaming a tag silently drops invites (or a
// redeemer's token) from an existing config instead of failing loudly.
func TestInviteJSONTags(t *testing.T) {
	conf, _ := newTestConfig(t)
	invite, err := conf.CreateInvite(CreateInviteParams{MaxUses: 1, Alias: "laptop"})
	require.NoError(t, err)
	// Under the lock, and read back through Export: the writer goroutine
	// marshals the same config concurrently.
	conf.Lock()
	conf.KnownPeers["peer-1"] = KnownPeer{
		PeerID:             "peer-1",
		InviteID:           invite.ID,
		PendingInviteToken: "s3cret-token",
	}
	conf.Unlock()

	data := conf.Export()
	for _, key := range []string{`"invites"`, `"pendingInviteToken"`, `"inviteID"`, `"maxUses"`, `"usedCount"`, `"expiresAt"`} {
		assert.True(t, strings.Contains(string(data), key), "config JSON is missing %s", key)
	}

	restored := &Config{}
	require.NoError(t, json.Unmarshal(data, restored))
	restoredInvite := restored.Invites[invite.ID]
	assert.Equal(t, invite.Token, restoredInvite.Token)
	assert.Equal(t, invite.Alias, restoredInvite.Alias)
	assert.Equal(t, invite.MaxUses, restoredInvite.MaxUses)
	// Times survive as RFC 3339, so compare instants, not struct internals.
	assert.True(t, invite.CreatedAt.Equal(restoredInvite.CreatedAt))
	assert.Equal(t, "s3cret-token", restored.KnownPeers["peer-1"].PendingInviteToken)
	assert.Equal(t, invite.ID, restored.KnownPeers["peer-1"].InviteID)
}

// reserveInvite takes the write lock the way the real callers do — inside the
// critical section that also writes the peer the invite is being spent on.
func reserveInvite(t *testing.T, conf *Config, token string) (Invite, error) {
	t.Helper()

	conf.Lock()
	defer conf.Unlock()

	return conf.ReserveInviteUnlocked(token)
}

func TestReserveInvite(t *testing.T) {
	t.Run("Valid", func(t *testing.T) {
		conf, _ := newTestConfig(t)
		created, err := conf.CreateInvite(CreateInviteParams{MaxUses: 1, Alias: "laptop"})
		require.NoError(t, err)

		reserved, err := reserveInvite(t, conf, created.Token)
		require.NoError(t, err)
		assert.Equal(t, created.ID, reserved.ID)
		assert.Equal(t, "laptop", reserved.Alias)

		stored, _ := conf.GetInvite(created.ID)
		assert.Equal(t, 1, stored.UsedCount)

		// single-use: the second attempt finds nothing left
		_, err = reserveInvite(t, conf, created.Token)
		assert.ErrorIs(t, err, ErrInviteUsedUp)
	})

	t.Run("MultiUse", func(t *testing.T) {
		conf, _ := newTestConfig(t)
		created, err := conf.CreateInvite(CreateInviteParams{MaxUses: 3})
		require.NoError(t, err)

		for range 3 {
			_, err = reserveInvite(t, conf, created.Token)
			require.NoError(t, err)
		}
		_, err = reserveInvite(t, conf, created.Token)
		assert.ErrorIs(t, err, ErrInviteUsedUp)
	})

	t.Run("Expired", func(t *testing.T) {
		conf, _ := newTestConfig(t)
		created, err := conf.CreateInvite(CreateInviteParams{
			MaxUses:   1,
			ExpiresAt: time.Now().Add(-time.Minute),
		})
		require.NoError(t, err)

		_, err = reserveInvite(t, conf, created.Token)
		assert.ErrorIs(t, err, ErrInviteExpired)

		stored, _ := conf.GetInvite(created.ID)
		assert.Equal(t, 0, stored.UsedCount, "a rejected invite must not be consumed")
	})

	t.Run("Revoked", func(t *testing.T) {
		conf, _ := newTestConfig(t)
		created, err := conf.CreateInvite(CreateInviteParams{MaxUses: 5})
		require.NoError(t, err)
		require.True(t, conf.RevokeInvite(created.ID))

		_, err = reserveInvite(t, conf, created.Token)
		assert.ErrorIs(t, err, ErrInviteRevoked)

		assert.False(t, conf.RevokeInvite("no-such-invite"))
	})

	t.Run("UnknownToken", func(t *testing.T) {
		conf, _ := newTestConfig(t)
		_, err := conf.CreateInvite(CreateInviteParams{MaxUses: 1})
		require.NoError(t, err)

		_, err = reserveInvite(t, conf, "not-a-real-token")
		assert.ErrorIs(t, err, ErrInviteNotFound)

		_, err = reserveInvite(t, conf, "")
		assert.ErrorIs(t, err, ErrInviteNotFound)
	})
}

// TestCheckInvite: the peek answers the same questions as the reservation but
// spends nothing — the auth handler uses it to pick a branch before it knows
// whether a peer will actually be added.
func TestCheckInvite(t *testing.T) {
	conf, _ := newTestConfig(t)
	created, err := conf.CreateInvite(CreateInviteParams{MaxUses: 1, Alias: "laptop"})
	require.NoError(t, err)

	checked, err := conf.CheckInvite(created.Token)
	require.NoError(t, err)
	assert.Equal(t, created.ID, checked.ID)

	stored, _ := conf.GetInvite(created.ID)
	assert.Equal(t, 0, stored.UsedCount, "checking must not consume a use")

	_, err = conf.CheckInvite("not-a-real-token")
	assert.ErrorIs(t, err, ErrInviteNotFound)

	_, err = reserveInvite(t, conf, created.Token)
	require.NoError(t, err)
	_, err = conf.CheckInvite(created.Token)
	assert.ErrorIs(t, err, ErrInviteUsedUp)
}

// TestSetDefaultsInitsInvites covers configs written before invites existed:
// the map is nil there, and writing into a nil map panics.
func TestSetDefaultsInitsInvites(t *testing.T) {
	conf := &Config{dataDir: t.TempDir()}
	setDefaults(conf, nil)

	require.NotNil(t, conf.Invites)
	assert.Empty(t, conf.Invites)
}
