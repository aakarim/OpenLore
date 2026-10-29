package lore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/lore/api"
)

// ServerURL validates a server origin and strips its trailing slash.
func ServerURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("provide an http(s) server URL, or set external_url in openlore.yml")
	}
	return strings.TrimRight(raw, "/"), nil
}

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// OAuthLogin runs the PKCE authorization-code flow against client's server
// with a loopback redirect, leaving the tokens in client.Credentials. open
// hands the authorization link to the person logging in.
func OAuthLogin(client *api.Client, open func(link string)) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	redirect := "http://" + listener.Addr().String() + "/callback"
	clientID, err := client.Register(redirect)
	if err != nil {
		listener.Close()
		return err
	}
	client.Credentials.ClientID = clientID
	state, verifier := randomString(), randomString()
	challenge := sha256.Sum256([]byte(verifier))
	values := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "scope": {"full"}}
	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state", 400)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "login rejected", 400)
			select {
			case codes <- "":
			default:
			}
			return
		}
		select {
		case codes <- code:
		default:
		}
		w.Write([]byte("Login received. You can close this tab.\n"))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	open(client.Credentials.Server + "/authorize?" + values.Encode())
	select {
	case code := <-codes:
		if code == "" {
			return fmt.Errorf("OAuth authorization rejected")
		}
		return client.Exchange(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "client_id": {clientID}})
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("login timed out")
	}
}
