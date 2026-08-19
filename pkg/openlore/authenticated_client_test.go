package openlore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/validator"
	"github.com/golang-jwt/jwt/v5"
)

type staticRoundTripper func(*http.Request) (*http.Response, error)

func (f staticRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func staticJSONClient(body []byte) *http.Client {
	return &http.Client{Transport: staticRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
}

func TestCIMDResolverPinsNameAndOrigin(t *testing.T) {
	r := newCIMDResolver()
	r.client = staticJSONClient([]byte(`{
		"client_name":"Claude Code",
		"redirect_uris":["http://localhost:1234/callback"],
		"token_endpoint_auth_methods_supported":["none"]
	}`))
	client, err := r.Resolve(context.Background(), "https://oauth.tools.claude.ai/client.json")
	if err != nil {
		t.Fatal(err)
	}
	if client.Origin != "claude.ai" || slugClientName(client.ClientName, client.Origin) != "claude-code" {
		t.Fatalf("client=%+v", client)
	}
	if !oauthClientFromCIMD(client).AllowsRedirect("http://localhost:9876/callback") {
		t.Fatal("CIMD loopback redirect should ignore the ephemeral port")
	}
	if oauthClientFromCIMD(client).AllowsRedirect("http://localhost:9876/other") {
		t.Fatal("loopback matching broadened the registered path")
	}
}

func TestCIMDResolverRejectsUnsafeInputs(t *testing.T) {
	r := newCIMDResolver()
	for _, clientID := range []string{"http://client.example/meta", "https://127.0.0.1/meta", "https://localhost/meta"} {
		if _, err := r.Resolve(context.Background(), clientID); err == nil {
			t.Fatalf("accepted unsafe CIMD URL %q", clientID)
		}
	}
	if publicInternetAddr(netipMustParse("10.0.0.1")) || publicInternetAddr(netipMustParse("169.254.1.2")) || publicInternetAddr(netipMustParse("100.64.1.2")) {
		t.Fatal("private or link-local address passed SSRF guard")
	}
}

func netipMustParse(raw string) netip.Addr {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		panic(err)
	}
	return addr
}

type fixedCIMDResolver struct{ client *CIMDClient }

func (r fixedCIMDResolver) Resolve(context.Context, string) (*CIMDClient, error) {
	return r.client, nil
}

func TestPrivateKeyJWTAuthenticationAndReplayProtection(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{jwkForTest(&key.PublicKey, "client-key")}})
	const clientID = "https://chatgpt.com/oauth/server/client.json"
	const endpoint = "https://openlore.example/oauth/token"
	client := &CIMDClient{ClientID: clientID, Origin: "chatgpt.com", ClientName: "ChatGPT", AuthMethods: []string{"none", "private_key_jwt"}, JWKSURI: "https://chatgpt.com/oauth/jwks.json"}
	authenticator := &clientAuthenticator{
		resolver: fixedCIMDResolver{client}, httpClient: staticJSONClient(jwks), tokenEndpoint: endpoint,
		validators: map[string]*validator.Validator{}, usedJTI: map[string]time.Time{},
	}
	claims := jwt.MapClaims{"iss": clientID, "sub": clientID, "aud": endpoint, "iat": time.Now().Unix(), "exp": time.Now().Add(2 * time.Minute).Unix(), "jti": "once"}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = "client-key"
	assertion, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"client_assertion_type": {clientAssertionType}, "client_assertion": {assertion}}
	req := httptest.NewRequest(http.MethodPost, tokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	if level, err := authenticator.Authenticate(req, client); err != nil || level != AuthPrivateKeyJWT {
		t.Fatalf("level=%q err=%v", level, err)
	}
	if _, err := authenticator.Authenticate(req, client); err == nil {
		t.Fatal("replayed jti was accepted")
	}
	claims["jti"] = "with-mtls"
	token = jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = "client-key"
	assertion, _ = token.SignedString(key)
	mtlsForm := url.Values{"client_assertion_type": {clientAssertionType}, "client_assertion": {assertion}}
	mtlsReq := httptest.NewRequest(http.MethodPost, tokenPath, strings.NewReader(mtlsForm.Encode()))
	mtlsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = mtlsReq.ParseForm()
	cert := &x509.Certificate{}
	mtlsReq.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	if level, err := authenticator.Authenticate(mtlsReq, client); err != nil || level != AuthPrivateKeyJWTMTLS {
		t.Fatalf("mTLS level=%q err=%v", level, err)
	}

	downgrade := httptest.NewRequest(http.MethodPost, tokenPath, nil)
	downgrade.Form = url.Values{}
	if _, err := authenticator.Authenticate(downgrade, client); err == nil {
		t.Fatal("private_key_jwt client downgraded to none")
	}
}

func jwkForTest(pub *ecdsa.PublicKey, kid string) map[string]string {
	size := (pub.Curve.Params().BitSize + 7) / 8
	return map[string]string{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
		"x": base64.RawURLEncoding.EncodeToString(leftPad(pub.X, size)),
		"y": base64.RawURLEncoding.EncodeToString(leftPad(pub.Y, size)),
	}
}

func TestIssuerRotationGraceAndEmergencyRevocation(t *testing.T) {
	issuer, err := newESIssuer("https://openlore.test", "https://openlore.test", filepath.Join(t.TempDir(), "auth", "es256.pem"))
	if err != nil {
		t.Fatal(err)
	}
	oldToken, _, _ := issuer.Mint(Attribution{Principal: "alice"}, ScopeFull, time.Minute)
	if _, err := issuer.Rotate(false); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Verify(oldToken); err != nil {
		t.Fatalf("graceful rotation invalidated old token: %v", err)
	}
	newToken, _, _ := issuer.Mint(Attribution{Principal: "alice"}, ScopeFull, time.Minute)
	if _, err := issuer.Rotate(true); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Verify(newToken); err == nil {
		t.Fatal("emergency rotation retained previous token")
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	b, _ := issuer.JWKS()
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatalf("JWKS after emergency rotation: %s, %v", b, err)
	}
}

func TestClientAuthClaimRoundTrip(t *testing.T) {
	issuer, err := newESIssuer("https://openlore.test", "https://openlore.test", filepath.Join(t.TempDir(), "auth", "es256.pem"))
	if err != nil {
		t.Fatal(err)
	}
	signed, _, err := issuer.Mint(Attribution{Principal: "alice", Actor: "chatgpt@chatgpt.com", ClientAuth: AuthPrivateKeyJWT}, ScopeFull, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.Verify(signed)
	if err != nil || claims.ClientAuth != AuthPrivateKeyJWT {
		t.Fatalf("client_auth=%q err=%v", claims.ClientAuth, err)
	}
}

func TestIssuerObservesRotationFromAnotherProcess(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "auth", "es256.pem")
	serverIssuer, err := newESIssuer("https://openlore.test", "https://openlore.test", keyPath)
	if err != nil {
		t.Fatal(err)
	}
	adminIssuer, err := newESIssuer("https://openlore.test", "https://openlore.test", keyPath)
	if err != nil {
		t.Fatal(err)
	}
	newKID, err := adminIssuer.Rotate(false)
	if err != nil {
		t.Fatal(err)
	}
	keySetPath := keyPath + ".keys.json"
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(keySetPath, future, future); err != nil {
		t.Fatal(err)
	}
	signed, _, err := serverIssuer.Mint(Attribution{Principal: "alice"}, ScopeFull, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(signed, jwt.MapClaims{})
	if err != nil || parsed.Header["kid"] != newKID {
		t.Fatalf("server did not adopt rotated key: kid=%v want=%s err=%v", parsed.Header["kid"], newKID, err)
	}
}

func TestRefreshTokenRevocationEndpoint(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code, _ := s.IssueAuthCode("alice", ScopeFull)
	_, issued := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}})
	req := httptest.NewRequest(http.MethodPost, revocationPath, strings.NewReader(url.Values{"token": {issued.RefreshToken}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.tokens.revoke(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := s.refreshStore.Lookup(issued.RefreshToken); ok {
		t.Fatal("revoked refresh chain remains in store")
	}
}
