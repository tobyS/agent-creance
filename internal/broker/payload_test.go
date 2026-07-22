package broker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/mint/githubapp"
	"github.com/tobyS/agent-creance/internal/mint/oauth2mint"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// TestPayloadRoundTrip: a static, a github_app, and an oauth2 spec survive fd-3 JSON
// marshalling and MinterFor builds the right minter (or none) for each.
func TestPayloadRoundTrip(t *testing.T) {
	in := Payload{
		"static": {Kind: KindStatic, Token: "tok"},
		"gh-app": {Kind: KindGitHubApp, GitHubApp: &GitHubAppSpec{
			PEM: "PEM", ClientID: "cid", Repo: "o/n", Permissions: map[string]string{"contents": "read"},
		}},
		"drive": {Kind: KindOAuth2, OAuth2: &OAuth2Spec{
			RefreshToken: "rt", ClientID: "cid", TokenEndpoint: "https://oauth2.googleapis.com/token",
			Scopes: []string{"https://www.googleapis.com/auth/drive.file"},
		}},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)
	var out Payload
	require.NoError(t, json.Unmarshal(data, &out))
	require.Equal(t, in, out)

	http := sysdeptest.NewFakeHTTPClient()
	clock := sysdeptest.NewFakeClock(time.Now())

	// Static → no minter.
	_, minted := MinterFor(out["static"], http, clock)
	require.False(t, minted)

	// github_app → a githubapp.Minter.
	ghm, minted := MinterFor(out["gh-app"], http, clock)
	require.True(t, minted)
	require.IsType(t, &githubapp.Minter{}, ghm)

	// oauth2 → an oauth2mint.Minter.
	om, minted := MinterFor(out["drive"], http, clock)
	require.True(t, minted)
	require.IsType(t, &oauth2mint.Minter{}, om)
}
