package openlore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	maxCIMDBytes = 64 << 10
	cimdTimeout  = 5 * time.Second
)

type ClientAuthLevel string

const (
	AuthPrivateKeyJWTMTLS ClientAuthLevel = "private_key_jwt+mtls"
	AuthPrivateKeyJWT     ClientAuthLevel = "private_key_jwt"
	AuthCIMD              ClientAuthLevel = "cimd"
	AuthDCRDomain         ClientAuthLevel = "dcr-domain"
	AuthDCRLocal          ClientAuthLevel = "dcr-local"
)

func (l ClientAuthLevel) Verified() bool {
	return l == AuthPrivateKeyJWTMTLS || l == AuthPrivateKeyJWT || l == AuthCIMD
}

type CIMDClient struct {
	ClientID     string    `json:"-"`
	Origin       string    `json:"-"`
	ClientName   string    `json:"client_name"`
	RedirectURIs []string  `json:"redirect_uris"`
	AuthMethods  []string  `json:"token_endpoint_auth_methods_supported"`
	JWKSURI      string    `json:"jwks_uri"`
	FetchedAt    time.Time `json:"-"`
	ExpiresAt    time.Time `json:"-"`
}

func (c CIMDClient) Offers(method string) bool {
	for _, candidate := range c.AuthMethods {
		if candidate == method {
			return true
		}
	}
	return false
}

type CIMDResolver interface {
	Resolve(context.Context, string) (*CIMDClient, error)
}

type cimdCacheEntry struct{ client *CIMDClient }

type cimdResolver struct {
	mu      sync.Mutex
	cache   map[string]cimdCacheEntry
	client  *http.Client
	resolve func(context.Context, string) ([]net.IPAddr, error)
}

func newCIMDResolver() *cimdResolver {
	r := &cimdResolver{cache: map[string]cimdCacheEntry{}}
	r.resolve = net.DefaultResolver.LookupIPAddr
	transport := &http.Transport{TLSHandshakeTimeout: cimdTimeout}
	transport.DialContext = r.safeDialContext
	r.client = &http.Client{
		Timeout:   cimdTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || !sameOrigin(via[0].URL, req.URL) {
				return errors.New("CIMD redirect changed origin")
			}
			return nil
		},
	}
	return r
}

func (r *cimdResolver) Resolve(ctx context.Context, clientID string) (*CIMDClient, error) {
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("CIMD client_id must be an HTTPS URL")
	}
	r.mu.Lock()
	entry, ok := r.cache[clientID]
	if ok && time.Now().Before(entry.client.ExpiresAt) {
		clone := *entry.client
		r.mu.Unlock()
		return &clone, nil
	}
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch CIMD: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch CIMD: status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxCIMDBytes+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(b) > maxCIMDBytes {
		return nil, errors.New("CIMD exceeds size limit")
	}
	var client CIMDClient
	if err := json.Unmarshal(b, &client); err != nil {
		return nil, fmt.Errorf("parse CIMD: %w", err)
	}
	if len(client.RedirectURIs) == 0 {
		return nil, errors.New("CIMD has no redirect_uris")
	}
	for _, redirect := range client.RedirectURIs {
		if !validRegisteredRedirectURI(redirect) {
			return nil, fmt.Errorf("CIMD has invalid redirect_uri %q", redirect)
		}
	}
	if len(client.AuthMethods) == 0 {
		client.AuthMethods = []string{"none"}
	}
	for _, method := range client.AuthMethods {
		if method != "none" && method != "private_key_jwt" {
			return nil, fmt.Errorf("CIMD has unsupported token auth method %q", method)
		}
	}
	if client.Offers("private_key_jwt") {
		jwksURL, err := url.Parse(client.JWKSURI)
		if err != nil || jwksURL.Scheme != "https" || !sameOrigin(u, jwksURL) {
			return nil, errors.New("private_key_jwt CIMD requires same-origin HTTPS jwks_uri")
		}
	}
	origin, err := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(u.Hostname()))
	if err != nil {
		return nil, fmt.Errorf("CIMD origin has no registrable domain: %w", err)
	}
	client.ClientID = clientID
	client.Origin = origin
	client.FetchedAt = time.Now().UTC()
	client.ExpiresAt = client.FetchedAt.Add(cacheMaxAge(resp.Header.Get("Cache-Control")))
	r.mu.Lock()
	r.cache[clientID] = cimdCacheEntry{client: &client}
	r.mu.Unlock()
	clone := client
	return &clone, nil
}

func (r *cimdResolver) Invalidate(clientID string) {
	r.mu.Lock()
	delete(r.cache, clientID)
	r.mu.Unlock()
}

func (r *cimdResolver) safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := r.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	for _, resolved := range addrs {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok || !publicInternetAddr(addr.Unmap()) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("CIMD host resolves only to blocked addresses")
}

func publicInternetAddr(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, blocked := range nonPublicPrefixes {
		if blocked.Contains(addr) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),  // shared address space
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func cacheMaxAge(value string) time.Duration {
	for _, directive := range strings.Split(value, ",") {
		name, raw, ok := strings.Cut(strings.TrimSpace(directive), "=")
		if ok && strings.EqualFold(name, "max-age") {
			seconds, err := strconv.Atoi(strings.Trim(raw, `"`))
			if err == nil && seconds > 0 {
				if seconds > 86400 {
					seconds = 86400
				}
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 15 * time.Minute
}

func slugClientName(name, origin string) string {
	slug := strings.ToLower(name)
	slug = regexpNonSlug.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = strings.Split(origin, ".")[0]
	}
	return slug
}

var regexpNonSlug = regexp.MustCompile(`[^a-z0-9]+`)
