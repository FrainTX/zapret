package probe

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"time"

	"prosvet/internal/lists"
)

var snRe = regexp.MustCompile(`=>\s*(sn-[a-z0-9-]+)`)

// GoogleVideoNode finds the YouTube cache node the ISP routes this client to
// (the one video segments actually come from). redirector.googlevideo.com
// answers with "<ip> => sn-xxxx".
func GoogleVideoNode(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://redirector.googlevideo.com/report_mapping?di=no", nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	m := snRe.FindSubmatch(b)
	if m == nil {
		return "redirector.googlevideo.com", nil
	}
	return "rr1---" + string(m[1]) + ".googlevideo.com", nil
}

// cloudHosts are well-known sites hosted at the providers the TSPU throttles,
// one or two per provider. Selection follows hyperion-cs/dpi-checkers
// (Apache-2.0), trimmed to hosts that answer a POST quickly.
var cloudHosts = []struct{ id, host, provider string }{
	{"aws-1", "optout.aboutads.info", "AWS"},
	{"aws-2", "amplifon.com", "AWS"},
	{"azure", "store.takeda.com", "Azure"},
	{"gcp", "api.usercentrics.eu", "Google Cloud"},
	{"cf-1", "esm.sh", "Cloudflare"},
	{"cf-2", "img.wzstats.gg", "Cloudflare"},
	{"hetzner", "king.hr", "Hetzner"},
	{"ovh", "www.adwin.fr", "OVH"},
	{"do", "ui-arts.com", "DigitalOcean"},
	{"akamai", "cdn.apple-mapkit.com", "Akamai"},
	{"fastly", "www.jetblue.com", "Fastly"},
	{"vultr", "askit-app.de", "Vultr"},
}

// Targets returns the probes for a group. custom are the user's domains.
func Targets(group string, custom []string) []Target {
	switch group {
	case "youtube":
		return []Target{
			{ID: "yt-web", Group: group, Title: "youtube.com", Kind: KindHTTPS, Host: "www.youtube.com", MinBytes: 48 * 1024},
			{ID: "yt-img", Group: group, Title: "превью i.ytimg.com", Kind: KindHTTPS, Host: "i.ytimg.com", Path: "/generate_204"},
			{ID: "yt-video", Group: group, Title: "видеосервер googlevideo", Kind: KindHTTPS, Host: "redirector.googlevideo.com", Path: "/generate_204", Discover: GoogleVideoNode},
			{ID: "yt-api", Group: group, Title: "youtubei API", Kind: KindHTTPS, Host: "youtubei.googleapis.com", Path: "/generate_204"},
		}
	case "youtube-quic":
		return []Target{
			{ID: "ytq-web", Group: group, Title: "youtube.com по QUIC", Kind: KindQUIC, Host: "www.youtube.com"},
			{ID: "ytq-video", Group: group, Title: "googlevideo по QUIC", Kind: KindQUIC, Host: "redirector.googlevideo.com", Discover: GoogleVideoNode},
		}
	case "discord":
		return []Target{
			{ID: "dc-web", Group: group, Title: "discord.com", Kind: KindHTTPS, Host: "discord.com", MinBytes: 24 * 1024},
			{ID: "dc-gw", Group: group, Title: "шлюз gateway.discord.gg", Kind: KindHTTPS, Host: "gateway.discord.gg"},
			{ID: "dc-cdn", Group: group, Title: "CDN cdn.discordapp.com", Kind: KindHTTPS, Host: "cdn.discordapp.com"},
			{ID: "dc-upd", Group: group, Title: "обновления", Kind: KindHTTPS, Host: "updates.discord.com"},
		}
	case "ubisoft":
		return []Target{
			{ID: "ubi-web", Group: group, Title: "ubisoft.com (CloudFront)", Kind: KindHTTPS, Host: "www.ubisoft.com", Path: "/en-us/", MinBytes: 32 * 1024},
			{ID: "ubi-svc", Group: group, Title: "public-ubiservices (AWS)", Kind: KindHTTPSPost, Host: "public-ubiservices.ubi.com", Path: "/v1/spaces/global/ubiconnect/news"},
			{ID: "ubi-connect", Group: group, Title: "connect.ubisoft.com", Kind: KindHTTPS, Host: "connect.ubisoft.com"},
			{ID: "ubi-cdn", Group: group, Title: "static3.cdn.ubi.com (Akamai)", Kind: KindHTTPS, Host: "static3.cdn.ubi.com"},
		}
	case "game-udp":
		return []Target{
			{ID: "gl-fra", Group: group, Title: "AWS Франкфурт · UDP", Kind: KindUDPEcho, Host: "gamelift-ping.eu-central-1.api.aws", Port: 7770},
			{ID: "gl-irl", Group: group, Title: "AWS Ирландия · UDP", Kind: KindUDPEcho, Host: "gamelift-ping.eu-west-1.api.aws", Port: 7770},
			{ID: "gl-sto", Group: group, Title: "AWS Стокгольм · UDP", Kind: KindUDPEcho, Host: "gamelift-ping.eu-north-1.api.aws", Port: 7770},
			{ID: "az-relay", Group: group, Title: "Microsoft/Azure · STUN", Kind: KindSTUN, Host: "worldaz.relay.teams.microsoft.com", Port: 3478},
		}
	case "cloud":
		var out []Target
		for _, h := range cloudHosts {
			out = append(out, Target{ID: "cl-" + h.id, Group: group, Title: h.provider + " · " + h.host, Kind: KindHTTPSPost, Host: h.host})
		}
		return out
	case "telegram":
		return []Target{
			{ID: "tg-web", Group: group, Title: "web.telegram.org", Kind: KindHTTPS, Host: "web.telegram.org", MinBytes: 8 * 1024},
			{ID: "tg-tme", Group: group, Title: "t.me", Kind: KindHTTPS, Host: "t.me"},
			{ID: "tg-org", Group: group, Title: "telegram.org", Kind: KindHTTPS, Host: "telegram.org", MinBytes: 16 * 1024},
		}
	case "sites":
		out := []Target{
			{ID: "doh-google", Group: group, Title: "DoH dns.google", Kind: KindHTTPS, Host: "dns.google", Path: "/resolve?name=example.com"},
		}
		for i, d := range lists.Merge(custom) {
			if i >= 6 {
				break
			}
			if d[0] == '^' {
				d = d[1:]
			}
			out = append(out, Target{ID: "site-" + d, Group: group, Title: d, Kind: KindHTTPS, Host: d})
		}
		return out
	}
	return nil
}
