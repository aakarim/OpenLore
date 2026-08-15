package skillsremote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/agentskills"
)

const MaxFiles = 1000

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

type Spec struct{ Repo, Path, Ref string }
type Refs struct {
	DefaultBranch  string
	Branches, Tags map[string]string
}
type Client struct {
	HTTP                     *http.Client
	GitHubBase, CodeloadBase string
	MaxBytes                 int64
	MaxFiles                 int
	MaxCompressedBytes       int64
	MaxArchiveBytes          int64
	MaxArchiveEntries        int
}

// NewPublicHTTPClient returns an HTTP client whose redirects and connections
// are restricted to HTTPS endpoints resolving to public IP addresses.
func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				if !isPublicIP(ip) {
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("remote host has no public IP address")
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("remote redirect must use HTTPS")
			}
			return nil
		},
	}
}

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func ParseSpec(raw string) (Spec, error) {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Spec{}, fmt.Errorf("remote must be an HTTPS repository URL")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return Spec{}, fmt.Errorf("missing owner/repo")
		}
		repo, err := agentskills.CanonicalRepoURL("https://" + u.Host + "/" + parts[0] + "/" + parts[1])
		if err != nil {
			return Spec{}, err
		}
		s := Spec{Repo: repo}
		canonicalURL, _ := url.Parse(repo)
		authority := canonicalURL.Host
		if authority == "github.com" && len(parts) >= 4 && parts[2] == "tree" {
			s.Ref = parts[3]
			s.Path = strings.Join(parts[4:], "/")
		} else if len(parts) >= 5 && parts[2] == "-" && parts[3] == "tree" {
			s.Ref = parts[4]
			s.Path = strings.Join(parts[5:], "/")
		} else if len(parts) >= 5 && parts[2] == "src" && (parts[3] == "branch" || parts[3] == "tag") {
			s.Ref = parts[4]
			s.Path = strings.Join(parts[5:], "/")
		} else if authority == "bitbucket.org" && len(parts) >= 4 && parts[2] == "src" {
			s.Ref = parts[3]
			s.Path = strings.Join(parts[4:], "/")
		} else if len(parts) != 2 {
			return Spec{}, fmt.Errorf("unsupported repository URL")
		}
		return s, validateSpec(s)
	}
	at := strings.LastIndex(raw, "@")
	if at >= 0 {
		raw, rawRef := raw[:at], raw[at+1:]
		raw = strings.Trim(raw, "/")
		s := splitShort(raw)
		s.Ref = rawRef
		return s, validateSpec(s)
	}
	s := splitShort(strings.Trim(raw, "/"))
	return s, validateSpec(s)
}
func splitShort(raw string) Spec {
	p := strings.Split(raw, "/")
	s := Spec{}
	if len(p) >= 2 {
		s.Repo, _ = agentskills.CanonicalRepoURL(p[0] + "/" + p[1])
		s.Path = strings.Join(p[2:], "/")
	}
	return s
}
func validateSpec(s Spec) error {
	if _, err := agentskills.CanonicalRepoURL(s.Repo); err != nil {
		return err
	}
	if err := agentskills.ValidateRemotePath(s.Path); err != nil {
		return fmt.Errorf("unsafe remote path")
	}
	if strings.ContainsAny(s.Ref, "\x00\r\n") {
		return fmt.Errorf("unsafe ref")
	}
	return nil
}

func (c *Client) defaults() {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.GitHubBase == "" {
		c.GitHubBase = "https://github.com"
	}
	if c.CodeloadBase == "" {
		c.CodeloadBase = "https://codeload.github.com"
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 10 * 1024 * 1024
	}
	if c.MaxFiles == 0 {
		c.MaxFiles = MaxFiles
	}
	if c.MaxArchiveBytes == 0 {
		c.MaxArchiveBytes = c.MaxBytes * 10
	}
	if c.MaxCompressedBytes == 0 {
		c.MaxCompressedBytes = c.MaxArchiveBytes
	}
	if c.MaxArchiveEntries == 0 {
		c.MaxArchiveEntries = c.MaxFiles * 10
	}
}
func (c *Client) Resolve(ctx context.Context, repo string) (Refs, error) {
	c.defaults()
	repoURL, err := agentskills.CanonicalRepoURL(repo)
	if err != nil {
		return Refs{}, err
	}
	base := repoURL
	if u, _ := url.Parse(repoURL); u.Host == "github.com" && c.GitHubBase != "https://github.com" {
		base = strings.TrimSuffix(c.GitHubBase, "/") + u.Path
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/info/refs?service=git-upload-pack", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Refs{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Refs{}, fmt.Errorf("git refs: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Refs{}, err
	}
	return parseRefs(b)
}
func parseRefs(b []byte) (Refs, error) {
	r := Refs{Branches: map[string]string{}, Tags: map[string]string{}}
	first := true
	for len(b) > 0 {
		if len(b) < 4 {
			return r, fmt.Errorf("invalid pkt-line")
		}
		var n int
		if _, e := fmt.Sscanf(string(b[:4]), "%04x", &n); e != nil {
			return r, e
		}
		b = b[4:]
		if n == 0 {
			continue
		}
		if n < 4 || n-4 > len(b) {
			return r, fmt.Errorf("invalid pkt-line length")
		}
		line := strings.TrimSuffix(string(b[:n-4]), "\n")
		b = b[n-4:]
		if strings.HasPrefix(line, "# service=") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		sha, name := fields[0], fields[1]
		if !regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(sha) {
			continue
		}
		if first {
			first = false
			caps := strings.SplitN(name, "\x00", 2)
			name = caps[0]
			if len(caps) > 1 {
				for _, cap := range strings.Fields(caps[1]) {
					if strings.HasPrefix(cap, "symref=HEAD:refs/heads/") {
						r.DefaultBranch = strings.TrimPrefix(cap, "symref=HEAD:refs/heads/")
					}
				}
			}
		}
		name = strings.SplitN(name, "\x00", 2)[0]
		if strings.HasPrefix(name, "refs/heads/") {
			r.Branches[strings.TrimPrefix(name, "refs/heads/")] = sha
		}
		if strings.HasPrefix(name, "refs/tags/") {
			key := strings.TrimPrefix(name, "refs/tags/")
			if strings.HasSuffix(key, "^{}") {
				r.Tags[strings.TrimSuffix(key, "^{}")] = sha
			} else if _, ok := r.Tags[key]; !ok {
				r.Tags[key] = sha
			}
		}
	}
	return r, nil
}
func (r Refs) Resolve(ref string) (sha, kind, resolvedRef string, err error) {
	if ref == "" {
		ref = r.DefaultBranch
	}
	if v := r.Branches[ref]; v != "" {
		return v, "tracking", ref, nil
	}
	if v := r.Tags[ref]; v != "" {
		return v, "pinned", ref, nil
	}
	if regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(ref) {
		return strings.ToLower(ref), "pinned", ref, nil
	}
	return "", "", ref, fmt.Errorf("ref %q not found", ref)
}

func (c *Client) Fetch(ctx context.Context, repo, sha, subtree string) (map[string][]byte, error) {
	c.defaults()
	archiveURLs, err := c.archiveURLs(repo, sha)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, archiveURL := range archiveURLs {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: %s", archiveURL, resp.Status)
			resp.Body.Close()
			continue
		}
		compressed, readErr := io.ReadAll(io.LimitReader(resp.Body, c.MaxCompressedBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(compressed)) > c.MaxCompressedBytes {
			return nil, fmt.Errorf("remote archive exceeds compressed size limit")
		}
		files, extractErr := c.extractArchive(compressed, subtree)
		if extractErr == nil {
			return files, nil
		}
		lastErr = fmt.Errorf("%s: %w", archiveURL, extractErr)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("remote archive unavailable")
	}
	return nil, lastErr
}

func (c *Client) extractArchive(compressed []byte, subtree string) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	decompressed := &io.LimitedReader{R: gz, N: c.MaxArchiveBytes + 1}
	tr := tar.NewReader(decompressed)
	files := map[string][]byte{}
	var total int64
	fileCount := 0
	archiveEntries := 0
	archiveRoot := ""
	archiveNames := map[string]byte{}
	for {
		h, e := tr.Next()
		if c.MaxArchiveBytes+1-decompressed.N > c.MaxArchiveBytes {
			return nil, fmt.Errorf("remote archive exceeds decompressed size limit")
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		archiveEntries++
		if archiveEntries > c.MaxArchiveEntries {
			return nil, fmt.Errorf("remote archive exceeds entry scan limit")
		}
		archiveName := h.Name
		if h.Typeflag == tar.TypeDir && strings.HasSuffix(archiveName, "/") {
			archiveName = strings.TrimSuffix(archiveName, "/")
		}
		clean := path.Clean(archiveName)
		if archiveName == "" || path.IsAbs(archiveName) || clean != archiveName || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(archiveName, "\\") {
			return nil, fmt.Errorf("unsafe archive path %q", h.Name)
		}
		if prior, duplicate := archiveNames[clean]; duplicate {
			return nil, fmt.Errorf("duplicate remote archive path %q (types %d and %d)", clean, prior, h.Typeflag)
		}
		archiveNames[clean] = h.Typeflag
		parts := strings.SplitN(clean, "/", 2)
		if len(parts) < 2 {
			continue
		}
		if archiveRoot == "" {
			archiveRoot = parts[0]
		} else if archiveRoot != parts[0] {
			return nil, fmt.Errorf("remote archive has multiple roots")
		}
		rel := parts[1]
		if subtree != "" {
			if rel == subtree {
				continue
			}
			if !strings.HasPrefix(rel, subtree+"/") {
				continue
			}
			rel = strings.TrimPrefix(rel, subtree+"/")
		}
		if hasPathComponent(rel, ".lore") {
			continue
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		fileCount++
		if fileCount > c.MaxFiles {
			return nil, fmt.Errorf("remote skill exceeds file count limit")
		}
		total += h.Size
		if total > c.MaxBytes {
			return nil, fmt.Errorf("remote skill exceeds size limit")
		}
		data, e := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if e != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("reading %s", rel)
		}
		if _, duplicate := files[rel]; duplicate {
			return nil, fmt.Errorf("duplicate remote file path %q", rel)
		}
		files[rel] = data
	}
	return files, nil
}

func (c *Client) archiveURLs(repo, sha string) ([]string, error) {
	repoURL, err := agentskills.CanonicalRepoURL(repo)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(repoURL)
	host := strings.ToLower(u.Host)
	ownerRepo := strings.TrimPrefix(u.Path, "/")
	repoName := path.Base(u.Path)
	switch host {
	case "github.com":
		return []string{strings.TrimSuffix(c.CodeloadBase, "/") + "/" + ownerRepo + "/tar.gz/" + sha}, nil
	case "gitlab.com":
		return []string{repoURL + "/-/archive/" + sha + "/" + repoName + "-" + sha + ".tar.gz"}, nil
	case "bitbucket.org":
		return []string{repoURL + "/get/" + sha + ".tar.gz"}, nil
	case "codeberg.org":
		return []string{repoURL + "/archive/" + sha + ".tar.gz"}, nil
	default:
		// GitLab and Gitea/Forgejo expose distinct, stable archive endpoints.
		// Trying both supports self-hosted installations without configuration.
		return []string{
			repoURL + "/-/archive/" + sha + "/" + repoName + "-" + sha + ".tar.gz",
			repoURL + "/archive/" + sha + ".tar.gz",
		}, nil
	}
}

func hasPathComponent(p, component string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == component {
			return true
		}
	}
	return false
}

// ValidateFiles rejects paths that cannot represent a conflict-free relative
// file tree. Callers run this before constructing a non-rollback batch.
func ValidateFiles(files map[string][]byte) error {
	for name := range files {
		if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "\\") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe remote file path %q", name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, conflict := files[parent]; conflict {
				return fmt.Errorf("remote file tree conflicts at %q", parent)
			}
		}
	}
	return nil
}
