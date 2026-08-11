package entity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPeerID = "12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ"

func TestBuildInviteLink(t *testing.T) {
	link := BuildInviteLink(testPeerID, "Xk9token", "Alice")
	assert.Equal(t, "awl://invite?p="+testPeerID+"&t=Xk9token&n=Alice", link)

	assert.Equal(t, "awl://invite?p="+testPeerID+"&t=Xk9token",
		BuildInviteLink(testPeerID, "Xk9token", ""))

	// No version parameter is ever written: it only has to appear the day the
	// format changes incompatibly.
	assert.NotContains(t, link, "v=")
}

// TestBuildInviteLinkWithoutToken: the same format doubles as the shareable
// form of a peer ID, so that everything reading a link — the add form, the QR
// scanner, the CLI — has one input to parse and not two.
func TestBuildInviteLinkWithoutToken(t *testing.T) {
	link := BuildInviteLink(testPeerID, "", "Alice")
	assert.Equal(t, "awl://invite?p="+testPeerID+"&n=Alice", link)

	parsed, err := ParseInviteLink(link)
	require.NoError(t, err)
	assert.Equal(t, testPeerID, parsed.PeerID)
	assert.Empty(t, parsed.Token, "no token means the peer is added by hand")
	assert.Equal(t, "Alice", parsed.Name)

	bare := BuildInviteLink(testPeerID, "", "")
	assert.Equal(t, "awl://invite?p="+testPeerID, bare)
	parsed, err = ParseInviteLink(bare)
	require.NoError(t, err)
	assert.Equal(t, testPeerID, parsed.PeerID)
	assert.Empty(t, parsed.Token)
	assert.Empty(t, parsed.Name)
}

// TestBuildInviteLinkEscapesName pins the wire form of the only parameter that
// can need escaping — a peer ID is base58/base32, a token base64url. It is the
// cross-language half of the format: the Dart implementation has to read back
// exactly this, a space written as "+" included.
func TestBuildInviteLinkEscapesName(t *testing.T) {
	link := BuildInviteLink(testPeerID, "tok", "Alice Smith")
	assert.Contains(t, link, "n=Alice+Smith")

	parsed, err := ParseInviteLink(link)
	require.NoError(t, err)
	assert.Equal(t, "Alice Smith", parsed.Name)

	// A literal plus is escaped, so it cannot come back as a space.
	link = BuildInviteLink(testPeerID, "tok", "a+b")
	assert.Contains(t, link, "n=a%2Bb")

	parsed, err = ParseInviteLink(link)
	require.NoError(t, err)
	assert.Equal(t, "a+b", parsed.Name)
}

func TestParseInviteLinkRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		token string
		peer  string
	}{
		{name: "Alice", token: "Xk9token"},
		{name: "", token: "with-_dashes"},
		{name: "имя с пробелом & амперсандом", token: "aGVsbG8gd29ybGQtLQ"},
	}

	for _, tt := range tests {
		link := BuildInviteLink(testPeerID, tt.token, tt.name)
		parsed, err := ParseInviteLink(link)
		require.NoError(t, err, link)
		assert.Equal(t, testPeerID, parsed.PeerID)
		assert.Equal(t, tt.token, parsed.Token)
		assert.Equal(t, tt.name, parsed.Name)
	}
}

// TestParseInviteLinkKeepsPeerIDCase is the regression for why the peer ID sits
// in the query and not in the authority: base58 is case-sensitive, hosts are
// not, and a lowercased peer ID no longer decodes.
func TestParseInviteLinkKeepsPeerIDCase(t *testing.T) {
	parsed, err := ParseInviteLink(BuildInviteLink(testPeerID, "token", ""))
	require.NoError(t, err)
	assert.Equal(t, testPeerID, parsed.PeerID)
}

func TestParseInviteLinkSurroundingWhitespace(t *testing.T) {
	link := BuildInviteLink(testPeerID, "token", "Alice")
	parsed, err := ParseInviteLink("  \n" + link + "\n\t")
	require.NoError(t, err)
	assert.Equal(t, testPeerID, parsed.PeerID)
}

// TestParseInviteLinkVersion: we never write v=, but we keep reading it, so
// that a link from a future incompatible format is rejected with a
// comprehensible message rather than a random parse failure.
func TestParseInviteLinkVersion(t *testing.T) {
	// A link without v= is the original format — that is every link we build.
	parsed, err := ParseInviteLink("awl://invite?p=" + testPeerID + "&t=token")
	require.NoError(t, err)
	assert.Equal(t, "token", parsed.Token)

	// v=1 stays acceptable: links were handed out with it while the format was
	// being settled.
	parsed, err = ParseInviteLink("awl://invite?p=" + testPeerID + "&t=token&v=1")
	require.NoError(t, err)
	assert.Equal(t, "token", parsed.Token)
}

func TestParseInviteLinkErrors(t *testing.T) {
	tests := []struct {
		name        string
		link        string
		errContains string
		notALink    bool
	}{
		{name: "Empty", link: "   ", notALink: true},
		{name: "Garbage", link: "just some text", notALink: true},
		{name: "PlainPeerID", link: testPeerID, notALink: true},
		{name: "OtherScheme", link: "https://invite?p=" + testPeerID + "&t=token", notALink: true},
		{name: "UnknownHost", link: "awl://peer?p=" + testPeerID + "&t=token", errContains: "unknown awl link type"},
		{name: "NoPeerID", link: "awl://invite?t=token&v=1", errContains: "no peer id"},
		{name: "InvalidPeerID", link: "awl://invite?p=not-a-peer-id&t=token&v=1", errContains: "invalid peer id"},
		{name: "NewerVersion", link: "awl://invite?p=" + testPeerID + "&t=token&v=2", errContains: "newer version of anywherelan"},
		{name: "BadVersion", link: "awl://invite?p=" + testPeerID + "&t=token&v=abc", errContains: "invalid invite link version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseInviteLink(tt.link)
			require.Error(t, err)
			if tt.notALink {
				assert.ErrorIs(t, err, ErrNotInviteLink)
			} else {
				assert.ErrorContains(t, err, tt.errContains)
			}
		})
	}
}
