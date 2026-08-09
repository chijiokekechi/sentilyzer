package connectors

import (
	"context"
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
	x := Credentials{YouTubeAPIKey: "ab", MastodonToken: ""}
	y := Credentials{YouTubeAPIKey: "a", MastodonToken: "b"}
	if x.Fingerprint() == y.Fingerprint() {
		t.Fatal("field boundaries must be part of the fingerprint")
	}

	// The fingerprint must be a fixed-size hash, never the value itself.
	leak := Credentials{MastodonToken: "super-secret-token"}
	if got := leak.Fingerprint(); len(got) != 64 {
		t.Fatalf("fingerprint should be a fixed-size hash, got %q", got)
	}
}

// The BYOK surface is deliberately YouTube + Mastodon only: X's Developer
// Agreement (III.G) and Reddit's Developer Terms (1.4) forbid handing keys to
// a third party, so those connectors must NOT implement CredentialedConnector.
func TestBYOKSurfaceIsExactlyYouTubeAndMastodon(t *testing.T) {
	client := &http.Client{}
	var conn Connector

	conn = NewYouTube(client, config.YouTubeCreds{})
	if _, ok := conn.(CredentialedConnector); !ok {
		t.Error("youtube should accept caller credentials")
	}
	conn = NewMastodon(client, config.MastodonCreds{Instance: "https://mastodon.social"})
	if _, ok := conn.(CredentialedConnector); !ok {
		t.Error("mastodon should accept caller credentials")
	}
	conn = NewReddit(client, config.RedditCreds{})
	if _, ok := conn.(CredentialedConnector); ok {
		t.Error("reddit must NOT accept caller credentials (Developer Terms 1.4)")
	}
	conn = NewTwitter(client, config.TwitterCreds{})
	if _, ok := conn.(CredentialedConnector); ok {
		t.Error("twitter must NOT accept caller credentials (Developer Agreement III.G)")
	}
}

func TestEnabledWith(t *testing.T) {
	client := &http.Client{}
	cases := []struct {
		name      string
		connector CredentialedConnector
		creds     Credentials
	}{
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

// A caller-supplied Mastodon instance is an outbound fetch target the caller
// controls; the SSRF guard must reject anything that isn't public https.
func TestValidateCallerInstance(t *testing.T) {
	ctx := context.Background()
	reject := []struct{ name, url string }{
		{"plain http", "http://mastodon.social"},
		{"loopback ip", "https://127.0.0.1"},
		{"loopback name", "https://localhost"},
		{"private 10.x", "https://10.0.0.8"},
		{"private 192.168", "https://192.168.1.1:8443"},
		{"cloud metadata", "https://169.254.169.254"},
		{"unspecified", "https://0.0.0.0"},
		{"garbage", "https://"},
		{"not a url", "::::"},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if err := validateCallerInstance(ctx, tc.url); err == nil {
				t.Fatalf("%s should be rejected", tc.url)
			}
		})
	}
	// A currently-nonexistent host must fail closed (does not resolve).
	if err := validateCallerInstance(ctx, "https://definitely-not-a-real-host.invalid"); err == nil {
		t.Fatal("unresolvable host should be rejected")
	}
}
