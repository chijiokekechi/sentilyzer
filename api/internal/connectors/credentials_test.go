package connectors

import (
	"net/http"
	"testing"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
)

func TestCredentialsFingerprint(t *testing.T) {
	zero := Credentials{}
	if !zero.IsZero() {
		t.Fatal("zero value should report IsZero")
	}
	if zero.Fingerprint() != "" {
		t.Fatalf("zero fingerprint = %q, want empty", zero.Fingerprint())
	}

	a := Credentials{YouTubeAPIKey: "key-a"}
	b := Credentials{YouTubeAPIKey: "key-b"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different credentials must fingerprint differently")
	}
	if a.Fingerprint() != (Credentials{YouTubeAPIKey: "key-a"}).Fingerprint() {
		t.Fatal("identical credentials must fingerprint identically")
	}

	// Field-boundary collision: values must not slide between fields.
	x := Credentials{RedditClientID: "ab", RedditClientSecret: ""}
	y := Credentials{RedditClientID: "a", RedditClientSecret: "b"}
	if x.Fingerprint() == y.Fingerprint() {
		t.Fatal("field boundaries must be part of the fingerprint")
	}

	// The fingerprint must never contain a credential value.
	leak := Credentials{TwitterBearerToken: "super-secret-token"}
	if got := leak.Fingerprint(); len(got) != 64 {
		t.Fatalf("fingerprint should be a fixed-size hash, got %q", got)
	}
}

// Every keyed connector must (a) implement CredentialedConnector, (b) be
// unlocked by caller creds without server config, and (c) stay disabled with
// neither.
func TestEnabledWith(t *testing.T) {
	client := &http.Client{}
	cases := []struct {
		name      string
		connector CredentialedConnector
		creds     Credentials
	}{
		{"reddit", NewReddit(client, config.RedditCreds{}),
			Credentials{RedditClientID: "id", RedditClientSecret: "sec"}},
		{"twitter", NewTwitter(client, config.TwitterCreds{}),
			Credentials{TwitterBearerToken: "tok"}},
		{"youtube", NewYouTube(client, config.YouTubeCreds{}),
			Credentials{YouTubeAPIKey: "key"}},
		{"mastodon", NewMastodon(client, config.MastodonCreds{Instance: "https://mastodon.social"}),
			Credentials{MastodonToken: "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ok, _ := tc.connector.Enabled(); ok {
				t.Fatal("connector without server keys should be disabled server-side")
			}
			if ok, reason := tc.connector.EnabledWith(tc.creds); !ok {
				t.Fatalf("caller creds should unlock the connector, got disabled: %s", reason)
			}
			if ok, _ := tc.connector.EnabledWith(Credentials{}); ok {
				t.Fatal("empty caller creds must not unlock an unconfigured connector")
			}
		})
	}
}

// Partial Reddit creds (id without secret) must not unlock the connector.
func TestEnabledWithPartialRedditCreds(t *testing.T) {
	r := NewReddit(&http.Client{}, config.RedditCreds{})
	if ok, _ := r.EnabledWith(Credentials{RedditClientID: "id-only"}); ok {
		t.Fatal("client id without secret must not enable reddit")
	}
	if ok, _ := r.EnabledWith(Credentials{RedditClientSecret: "secret-only"}); ok {
		t.Fatal("secret without client id must not enable reddit")
	}
}
