package newsfeed

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchOnceWithoutCredentialsOrLinkFollowing(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "synthetic-secret")
	raw := apiFixture(t)
	calls := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != nasaAPIURL(1) || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("fetch changed source coordinates or forwarded credentials")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > fetchTimeout {
			t.Fatal("fetch did not bound the request duration")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	snapshot, err := fetch(context.Background(), "nasa_news_releases_api", 1, transport)
	if err != nil || calls != 1 || snapshot.RawJSON != string(raw) || len(snapshot.Items) != 1 {
		t.Fatalf("capture failed or retried: %v, calls=%d", err, calls)
	}
	configured := newTransport()
	defer configured.CloseIdleConnections()
	if configured.Proxy != nil || !configured.DisableKeepAlives || !configured.DisableCompression || configured.TLSClientConfig != nil {
		t.Fatal("transport changed proxy, connection reuse, compression, or default TLS policy")
	}
}

func TestFetchRejectsErrorsRedirectsAndBoundsWithoutRetry(t *testing.T) {
	for _, mode := range []string{"error", "redirect", "status", "length", "body", "compressed", "malformed", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "error" {
					return nil, errors.New("synthetic-private-upstream-error")
				}
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(apiFixture(t))))}
				switch mode {
				case "redirect":
					response.StatusCode = 302
					response.Header.Set("Location", "https://example.invalid/must-not-follow")
				case "status":
					response.StatusCode = 500
				case "length":
					response.ContentLength = maxAPIBytes + 1
				case "body":
					response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", maxAPIBytes+1)))
				case "compressed":
					response.Header.Set("Content-Encoding", "gzip")
				case "malformed":
					response.Body = io.NopCloser(strings.NewReader("synthetic-private-not-xml"))
				case "timeout", "cancel":
					if mode == "cancel" {
						cancel()
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return response, nil
			})
			_, err := fetch(ctx, "nasa_news_releases_api", 1, transport)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("failure retried or exposed source detail: %v, calls=%d", err, calls)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) || mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
		})
	}
}

func TestFetchInvalidInputAndDisabledPublicSourceMakeNoRequest(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached transport")
		return nil, nil
	})
	for _, args := range []struct {
		id string
		n  int
	}{{"unknown", 1}, {"bbc_world", 1}, {"nasa_news_releases", 1}, {"nasa_news_releases_api", 0}, {"nasa_news_releases_api", 11}} {
		if _, err := fetch(context.Background(), args.id, args.n, transport); err == nil {
			t.Fatal("invalid source request accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetch(ctx, "nasa_news_releases_api", 1, transport); !errors.Is(err, context.Canceled) {
		t.Fatal("already cancelled request accepted")
	}
	if _, err := Fetch(context.Background(), "bbc_world", 1); err == nil {
		t.Fatal("public BBC source was enabled before usage approval")
	}
	if _, err := Fetch(context.Background(), "nasa_news_releases", 1); err == nil {
		t.Fatal("oversized legacy NASA RSS was fetched again")
	}
	// An already-cancelled public NASA invocation must also stop before DNS or
	// HTTP. Successful NASA responses are tested only with the in-memory helper.
	if _, err := Fetch(ctx, "nasa_news_releases_api", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled NASA operation reached the public transport")
	}
}

func TestPublicDialRejectsNonPublicDestinations(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "198.18.0.1", "192.0.2.1", "::1", "::", "fe80::1", "fc00::1", "ff02::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		t.Run(raw, func(t *testing.T) {
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("151.101.0.81"), netip.MustParseAddr(raw)}, nil
			}
			dial := func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("non-public DNS answer reached dial")
				return nil, nil
			}
			if _, err := dialResolved(context.Background(), "tcp", "www.nasa.gov:443", lookup, dial); err == nil {
				t.Fatal("non-public DNS destination accepted")
			}
		})
	}
	lookups, dials := 0, 0
	lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
		lookups++
		if network != "ip" || host != "www.nasa.gov" {
			t.Fatal("unexpected DNS lookup")
		}
		return []netip.Addr{netip.MustParseAddr("151.101.0.81"), netip.MustParseAddr("151.101.64.81")}, nil
	}
	stop := errors.New("synthetic dial stop")
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		if network != "tcp" || address != "151.101.0.81:443" {
			t.Fatal("dial performed another DNS resolution")
		}
		return nil, stop
	}
	if _, err := dialResolved(context.Background(), "tcp", "www.nasa.gov:443", lookup, dial); !errors.Is(err, stop) || lookups != 1 || dials != 1 {
		t.Fatal("public destination was not dialed exactly once")
	}
	if _, err := dialResolved(context.Background(), "tcp", "example.invalid:443", lookup, dial); err == nil || lookups != 1 || dials != 1 {
		t.Fatal("arbitrary host reached DNS or dial")
	}
	if _, err := dialResolved(context.Background(), "tcp", "feeds.bbci.co.uk:443", lookup, dial); err == nil || lookups != 1 || dials != 1 {
		t.Fatal("disabled BBC host reached DNS or dial")
	}
}
