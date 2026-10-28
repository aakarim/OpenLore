// Package api is the lore client's HTTP adapter for an OpenLore server: the
// docstore, shell and OAuth endpoints it uses.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/openlore"
)

// Credentials is what lore login saves for one knowledge repository.
type Credentials struct {
	Server       string `json:"server"`
	Identity     string `json:"identity"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
}

// Client talks to the server named in Credentials.
type Client struct {
	// APIPath is the server's JSON API prefix (config.Config.APIPath).
	APIPath     string
	Credentials Credentials
	// OnRefresh receives the renewed credentials after a refresh-token
	// exchange so the caller can persist them.
	OnRefresh func(Credentials) error
}

// StoreSnapshot is a docstore listing entry.
type StoreSnapshot struct {
	Store  string                  `json:"store"`
	Latest openlore.SourceSnapshot `json:"latest"`
}

// PendingSnapshot is a prepared snapshot and the blobs the server lacks.
type PendingSnapshot struct {
	Snapshot string   `json:"snapshot"`
	Missing  []string `json:"missing"`
}

// CommittedSnapshot is the server's commit response.
type CommittedSnapshot struct {
	Snapshot string `json:"snapshot"`
	Deleted  int    `json:"deleted"`
}

// ShellResult is the output of a server-side shell command.
type ShellResult struct {
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	IsError bool   `json:"is_error"`
}

func (c *Client) docstore() string  { return "/" + strings.Trim(c.APIPath, "/") + "/docstore/" }
func storePath(store string) string { return (&url.URL{Path: store}).EscapedPath() }

// Identity returns the identity the server attributes the credentials to.
func (c *Client) Identity() (string, error) {
	var who struct {
		Identity string `json:"identity"`
	}
	if err := c.Request("GET", c.docstore()+"identity", nil, &who); err != nil {
		return "", err
	}
	if who.Identity == "" {
		return "", fmt.Errorf("server returned no identity")
	}
	return who.Identity, nil
}

// Stores lists every store with its latest snapshot.
func (c *Client) Stores() ([]StoreSnapshot, error) {
	var remote []StoreSnapshot
	err := c.Request("GET", c.docstore(), nil, &remote)
	return remote, err
}

// Snapshots returns a store's snapshot history.
func (c *Client) Snapshots(store string) ([]openlore.SourceSnapshot, error) {
	var history []openlore.SourceSnapshot
	err := c.Request("GET", c.docstore()+storePath(store)+"/snapshots", nil, &history)
	return history, err
}

// PrepareSnapshot proposes a snapshot; dry only reports what it would need.
func (c *Client) PrepareSnapshot(store string, req openlore.SnapshotRequest, dry bool) (PendingSnapshot, error) {
	var pending PendingSnapshot
	b, err := json.Marshal(req)
	if err != nil {
		return pending, err
	}
	endpoint := c.docstore() + storePath(store) + "/snapshots"
	if dry {
		endpoint += "?dry_run=true"
	}
	err = c.Request("POST", endpoint, bytes.NewReader(b), &pending)
	return pending, err
}

// UploadBlob stores content addressed by its hash.
func (c *Client) UploadBlob(hash string, data []byte) error {
	return c.Request("PUT", c.docstore()+"blobs/"+hash, bytes.NewReader(data), nil)
}

// CommitSnapshot finalises a prepared snapshot.
func (c *Client) CommitSnapshot(store, snapshot string) (CommittedSnapshot, error) {
	var committed CommittedSnapshot
	err := c.Request("POST", c.docstore()+storePath(store)+"/snapshots/"+snapshot+"/commit", nil, &committed)
	return committed, err
}

// ShellSnippet runs a server shell command and returns its output.
func (c *Client) ShellSnippet(command string) (ShellResult, error) {
	var result ShellResult
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return result, err
	}
	err = c.Request("POST", "/"+strings.Trim(c.APIPath, "/")+"/shell", bytes.NewReader(b), &result)
	return result, err
}

// Register performs dynamic OAuth client registration for the loopback
// redirect and returns the client ID.
func (c *Client) Register(redirectURI string) (string, error) {
	reg, _ := json.Marshal(map[string]any{"client_name": "lore CLI", "redirect_uris": []string{redirectURI}, "token_endpoint_auth_method": "none"})
	var registered struct {
		ID string `json:"client_id"`
	}
	if err := c.Request("POST", "/register", bytes.NewReader(reg), &registered); err != nil {
		return "", err
	}
	if registered.ID == "" {
		return "", fmt.Errorf("registration returned no client ID")
	}
	return registered.ID, nil
}

// Exchange posts to the token endpoint and stores the tokens it returns.
func (c *Client) Exchange(values url.Values) error {
	h := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := h.PostForm(c.Credentials.Server+"/oauth/token", values)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("OAuth token exchange rejected (HTTP %d)", resp.StatusCode)
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}
	if tok.Access == "" {
		return fmt.Errorf("OAuth response missing access_token")
	}
	c.Credentials.Token = tok.Access
	if tok.Refresh != "" {
		c.Credentials.RefreshToken = tok.Refresh
	}
	return nil
}

// Request sends a JSON request with the bearer token, refreshing it once on
// 401 when a refresh token is held, and decodes the response into result.
func (c *Client) Request(method, endpoint string, body io.Reader, result any) error {
	return c.requestRetry(method, endpoint, body, result, true)
}
func (c *Client) requestRetry(method, endpoint string, body io.Reader, result any, refresh bool) error {
	// Retain a bounded body so a token refresh can retry the same operation.
	var data []byte
	if body != nil {
		var err error
		data, err = io.ReadAll(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.Credentials.Server, "/")+endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Credentials.Token)
	req.Header.Set("Content-Type", "application/json")
	h := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 && refresh && c.Credentials.RefreshToken != "" {
		if err := c.Exchange(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Credentials.RefreshToken}, "client_id": {c.Credentials.ClientID}}); err != nil {
			return err
		}
		if c.OnRefresh != nil {
			if err := c.OnRefresh(c.Credentials); err != nil {
				return err
			}
		}
		return c.requestRetry(method, endpoint, bytes.NewReader(data), result, false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server rejected %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}
