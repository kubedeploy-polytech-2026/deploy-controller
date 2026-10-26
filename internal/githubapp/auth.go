package githubapp

import (
	"bytes"
	"context"
	"crypto"
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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AppID          int64
	InstallationID int64
	PrivateKeyFile string
}

func FromEnv(getenv func(string) string) (Config, bool, error) {
	id, installation, key := getenv("GITHUB_APP_ID"), getenv("GITHUB_APP_INSTALLATION_ID"), getenv("GITHUB_APP_PRIVATE_KEY_FILE")
	if id == "" && installation == "" && key == "" {
		return Config{}, false, nil
	}
	appID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || appID <= 0 {
		return Config{}, true, fmt.Errorf("GITHUB_APP_ID must be a positive integer")
	}
	installationID, err := strconv.ParseInt(installation, 10, 64)
	if err != nil || installationID <= 0 {
		return Config{}, true, fmt.Errorf("GITHUB_APP_INSTALLATION_ID must be a positive integer")
	}
	if key == "" {
		return Config{}, true, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE is required")
	}
	return Config{AppID: appID, InstallationID: installationID, PrivateKeyFile: key}, true, nil
}

type installationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Source struct {
	config Config
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	tokens map[string]installationToken
}

func New(config Config) (*Source, error) {
	if config.AppID <= 0 || config.InstallationID <= 0 || config.PrivateKeyFile == "" {
		return nil, fmt.Errorf("incomplete GitHub App configuration")
	}
	if _, err := readKey(config.PrivateKeyFile); err != nil {
		return nil, err
	}
	return &Source{config: config, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now, tokens: make(map[string]installationToken)}, nil
}

func readKey(path string) (*rsa.PrivateKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read GitHub App private key")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, fmt.Errorf("cannot read GitHub App private key")
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("expected one RSA private key in PEM format")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				err = fmt.Errorf("not RSA")
			}
		}
	default:
		err = fmt.Errorf("unsupported key type")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("expected an RSA private key of at least 2048 bits")
	}
	if err = key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid GitHub App RSA private key")
	}
	return key, nil
}

func (s *Source) jwt(now time.Time) (string, error) {
	key, err := readKey(s.config.PrivateKeyFile)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(s.config.AppID, 10)})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("cannot sign GitHub App JWT")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *Source) Token(ctx context.Context, repository string) (string, error) {
	permissions := map[string]string{"contents": "read"}
	switch repository {
	case "environment":
		permissions["deployments"] = "write"
	case "backend", "frontend":
		permissions["attestations"] = "read"
	default:
		return "", fmt.Errorf("repository is not allowed for GitHub App authentication")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cached := s.tokens[repository]; cached.Token != "" && cached.ExpiresAt.After(s.now().Add(2*time.Minute)) {
		return cached.Token, nil
	}
	jwt, err := s.jwt(s.now())
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"repositories": []string{repository}, "permissions": permissions})
	endpoint := "https://api.github.com/app/installations/" + strconv.FormatInt(s.config.InstallationID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("cannot build installation token request")
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub App token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub App token request returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return "", fmt.Errorf("invalid installation token response")
	}
	var token installationToken
	if err = json.Unmarshal(raw, &token); err != nil || strings.TrimSpace(token.Token) == "" || strings.ContainsAny(token.Token, "\r\n\x00") || !token.ExpiresAt.After(s.now().Add(2*time.Minute)) {
		return "", fmt.Errorf("invalid or expired installation token")
	}
	s.tokens[repository] = token
	return token.Token, nil
}
