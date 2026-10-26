package githubapp

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func keyFile(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func source(t *testing.T) (*Source, *rsa.PrivateKey, *atomic.Int64) {
	t.Helper()
	path, key := keyFile(t)
	s, err := New(Config{AppID: 123, InstallationID: 456, PrivateKeyFile: path})
	if err != nil {
		t.Fatal(err)
	}
	clock := new(atomic.Int64)
	clock.Store(time.Now().Unix())
	s.now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	return s, key, clock
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func routeTo(t *testing.T, s *Source, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || r.URL.Path != "/app/installations/456/access_tokens" {
			t.Errorf("unexpected installation token destination")
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		copy.URL = &u
		return http.DefaultTransport.RoundTrip(copy)
	})
}

func verifyJWT(t *testing.T, raw string, key *rsa.PublicKey, now time.Time) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Error("invalid JWT shape")
		return
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h map[string]string
	if err := json.Unmarshal(header, &h); err != nil || h["alg"] != "RS256" || h["typ"] != "JWT" {
		t.Error("invalid JWT header")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Issued  int64  `json:"iat"`
		Expires int64  `json:"exp"`
		Issuer  string `json:"iss"`
	}
	if err := json.Unmarshal(claims, &c); err != nil {
		t.Error("invalid JWT claims")
	}
	if c.Issuer != "123" || c.Issued != now.Add(-time.Minute).Unix() || c.Expires != now.Add(9*time.Minute).Unix() {
		t.Error("incorrect JWT identity or lifetime")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Error("invalid JWT signature encoding")
		return
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], sig); err != nil {
		t.Error("invalid JWT signature")
	}
}

func TestScopedTokensCacheAndRefresh(t *testing.T) {
	s, key, clock := source(t)
	requests := make(chan struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}, 10)
	var calls atomic.Int64
	routeTo(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("incorrect token request")
		}
		verifyJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &key.PublicKey, s.now())
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationToken{Token: fmt.Sprintf("ghs_123_JWT_%d", calls.Add(1)), ExpiresAt: s.now().Add(time.Hour)})
	})
	for _, repo := range []string{"environment", "backend", "frontend"} {
		first, err := s.Token(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.Token(context.Background(), repo)
		if err != nil || second != first {
			t.Fatal("cached token not reused")
		}
		body := <-requests
		want := map[string]string{"contents": "read", "attestations": "read"}
		if repo == "environment" {
			want = map[string]string{"contents": "read", "deployments": "write"}
		}
		if !reflect.DeepEqual(body.Repositories, []string{repo}) || !reflect.DeepEqual(body.Permissions, want) {
			t.Fatalf("incorrect token scope: %+v", body)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected token requests")
	}
	clock.Add(int64(59 * time.Minute / time.Second))
	if _, err := s.Token(context.Background(), "backend"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatal("token was not refreshed before expiry")
	}
	if _, err := s.Token(context.Background(), "attacker"); err == nil {
		t.Fatal("accepted foreign repository")
	}
}

func TestConcurrentCallsMintOneToken(t *testing.T) {
	s, _, _ := source(t)
	var calls atomic.Int64
	routeTo(t, s, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationToken{Token: "short-lived-token", ExpiresAt: s.now().Add(time.Hour)})
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Token(context.Background(), "backend"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("minted %d tokens for concurrent callers", calls.Load())
	}
}

func TestRefreshFailureDoesNotReturnOldTokenOrResponseBody(t *testing.T) {
	s, _, clock := source(t)
	var fail atomic.Bool
	routeTo(t, s, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(403)
			io.WriteString(w, "private-response-must-not-leak")
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationToken{Token: "old-token", ExpiresAt: s.now().Add(time.Hour)})
	})
	if _, err := s.Token(context.Background(), "backend"); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	clock.Add(int64(59 * time.Minute / time.Second))
	token, err := s.Token(context.Background(), "backend")
	if err == nil || token != "" || strings.Contains(err.Error(), "private-response") {
		t.Fatal("refresh failure returned cached credentials or leaked response")
	}
}

func TestInvalidResponsesAndRedirect(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `invalid`, `{"token":"\nsecret"}`, `{"token":"secret","expires_at":"2020-01-01T00:00:00Z"}`, strings.Repeat("x", 65537)} {
		t.Run(fmt.Sprintf("length-%d", len(body)), func(t *testing.T) {
			s, _, _ := source(t)
			routeTo(t, s, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, body)
			})
			if token, err := s.Token(context.Background(), "backend"); err == nil || token != "" {
				t.Fatal("accepted invalid token response")
			}
		})
	}
	s, _, _ := source(t)
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	routeTo(t, s, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	if _, err := s.Token(context.Background(), "backend"); err == nil || redirected.Load() {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestCancellation(t *testing.T) {
	s, _, _ := source(t)
	routeTo(t, s, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Token(ctx, "backend"); err == nil {
		t.Fatal("cancelled token request succeeded")
	}
}

func TestKeyFormatsAndRotation(t *testing.T) {
	s, key, _ := source(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.config.PrivateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
		t.Fatal(err)
	}
	jwt, err := s.jwt(s.now())
	if err != nil {
		t.Fatal(err)
	}
	verifyJWT(t, jwt, &key.PublicKey, s.now())
	path, newKey := keyFile(t)
	raw, _ := os.ReadFile(path)
	if err = os.WriteFile(s.config.PrivateKeyFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	jwt, err = s.jwt(s.now())
	if err != nil {
		t.Fatal(err)
	}
	verifyJWT(t, jwt, &newKey.PublicKey, s.now())
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecBytes, _ := x509.MarshalPKCS8PrivateKey(ec)
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	for _, bad := range [][]byte{[]byte("not a key"), append(raw, raw...), bytesOfSize(65537), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecBytes}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)})} {
		os.WriteFile(s.config.PrivateKeyFile, bad, 0600)
		if _, err = s.jwt(s.now()); err == nil {
			t.Fatal("accepted invalid or weak private key")
		}
	}
	os.Remove(s.config.PrivateKeyFile)
	if _, err = s.jwt(s.now()); err == nil {
		t.Fatal("missing key ignored")
	}
}

func bytesOfSize(n int) []byte { return []byte(strings.Repeat("x", n)) }

func TestConfiguration(t *testing.T) {
	valid := map[string]string{"GITHUB_APP_ID": "123", "GITHUB_APP_INSTALLATION_ID": "456", "GITHUB_APP_PRIVATE_KEY_FILE": "/mounted/app.pem"}
	if _, enabled, err := FromEnv(func(string) string { return "" }); err != nil || enabled {
		t.Fatal("default local auth was changed")
	}
	if c, enabled, err := FromEnv(func(k string) string { return valid[k] }); err != nil || !enabled || c.AppID != 123 || c.InstallationID != 456 {
		t.Fatal("valid config rejected")
	}
	for _, key := range []string{"GITHUB_APP_ID", "GITHUB_APP_INSTALLATION_ID", "GITHUB_APP_PRIVATE_KEY_FILE"} {
		if _, enabled, err := FromEnv(func(k string) string {
			if k == key {
				return ""
			}
			return valid[k]
		}); err == nil || !enabled {
			t.Fatal("partial config fell back to local credentials")
		}
	}
	for _, id := range []string{"0", "-1", "bad"} {
		if _, _, err := FromEnv(func(k string) string {
			if k == "GITHUB_APP_ID" {
				return id
			}
			return valid[k]
		}); err == nil {
			t.Fatal("invalid app id accepted")
		}
	}
}
