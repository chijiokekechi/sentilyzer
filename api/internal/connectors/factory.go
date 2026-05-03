package connectors

import (
	"net/http"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
)

// BuildRegistry constructs the default connector set, taking credentials and
// switches from cfg. The Mock connector is always registered so that demos
// work out of the box; callers can hide it via UseMock=false at the service
// layer if desired.
func BuildRegistry(cfg *config.Config) *Registry {
	httpClient := &http.Client{Timeout: 12 * time.Second}
	reg := NewRegistry()

	reg.Register(NewHackerNews(httpClient))
	reg.Register(NewRSS(httpClient, nil))
	reg.Register(NewStockTwits(httpClient))
	reg.Register(NewReddit(httpClient, cfg.Reddit))
	reg.Register(NewTwitter(httpClient, cfg.Twitter))
	reg.Register(NewMastodon(httpClient, cfg.Mastodon))
	reg.Register(NewYouTube(httpClient, cfg.YouTube))

	if cfg.UseMock {
		reg.Register(Mock{})
	}
	return reg
}
