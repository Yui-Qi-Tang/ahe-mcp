package newsfeed

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

const fetchTimeout = 20 * time.Second

// Fetch reads one fixed first page from NASA's public news-release API.
// Historical RSS sources remain offline-only; links and pagination are not followed.
func Fetch(ctx context.Context, feedID string, limit int) (Snapshot, error) {
	if feedID != nasaAPIID {
		return Snapshot{}, errors.New("public news feed access is not enabled for this source")
	}
	transport := newTransport()
	defer transport.CloseIdleConnections()
	return fetch(ctx, feedID, limit, transport)
}

func newTransport() *http.Transport {
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		DialContext: publicDial, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10}
}

func fetch(ctx context.Context, feedID string, limit int, transport http.RoundTripper) (Snapshot, error) {
	bad := errors.New("public news feed fetch did not complete")
	if ctx == nil || feedID != nasaAPIID || limit < 1 || limit > 10 {
		return Snapshot{}, bad
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("news feed redirects are not permitted")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nasaAPIURL(limit), nil)
	if err != nil {
		return Snapshot{}, bad
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Detective-Public-News-Preview/0.1")
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, bad
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxAPIBytes || response.Header.Get("Content-Encoding") != "" {
		return Snapshot{}, bad
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIBytes+1))
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	if err != nil || len(raw) > maxAPIBytes {
		return Snapshot{}, bad
	}
	return NewAPISnapshot(feedID, raw, limit, time.Now())
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	return dialResolved(ctx, network, address, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}

func dialResolved(ctx context.Context, network, address string,
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	bad := errors.New("news feed destination is not an approved public address")
	if address != "www.nasa.gov:443" || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, bad
	}
	addresses, err := lookup(ctx, "ip", "www.nasa.gov")
	if err != nil || len(addresses) == 0 {
		return nil, bad
	}
	for _, ip := range addresses {
		if !publicIP(ip) {
			return nil, bad
		}
	}
	// Connect once to an already-checked address; no second DNS resolution or
	// fallback attempts can redirect this dial into a local/private destination.
	return dial(ctx, network, net.JoinHostPort(addresses[0].String(), "443"))
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
