package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/util"
)

type OAuthLoginService struct {
	mu       sync.RWMutex
	store    interface {
		LoadJSONDocument(name string) (any, error)
		SaveJSONDocument(name string, value any) error
	}
	providers map[string]OAuthProvider
}

type OAuthProvider struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	RedirectURL  string
	Scopes       []string
}

type OAuthState struct {
	State      string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	CreatedAt  string `json:"created_at"`
}

func NewOAuthLoginService(store interface {
	LoadJSONDocument(name string) (any, error)
	SaveJSONDocument(name string, value any) error
}) *OAuthLoginService {
	return &OAuthLoginService{
		store:     store,
		providers: map[string]OAuthProvider{},
	}
}

func (s *OAuthLoginService) AddProvider(provider OAuthProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[provider.Name] = provider
}

func (s *OAuthLoginService) ListProviders() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]map[string]any, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, map[string]any{
			"name":         p.Name,
			"auth_url":     p.AuthURL,
			"redirect_url": p.RedirectURL,
		})
	}
	return out
}

func (s *OAuthLoginService) GenerateAuthURL(providerName string, state string) (string, error) {
	s.mu.RLock()
	p, ok := s.providers[providerName]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown OAuth provider: %s", providerName)
	}

	verifier, challenge, err := generateOAuthPKCE()
	if err != nil {
		return "", err
	}

	oauthState := OAuthState{
		State:        state,
		CodeVerifier: verifier,
		CreatedAt:    util.NowISO(),
	}
	if err := s.saveOAuthState(state, oauthState); err != nil {
		return "", err
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", p.ClientID)
	params.Set("redirect_uri", p.RedirectURL)
	params.Set("state", state)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	if len(p.Scopes) > 0 {
		params.Set("scope", strings.Join(p.Scopes, " "))
	}

	return p.AuthURL + "?" + params.Encode(), nil
}

func (s *OAuthLoginService) ExchangeCode(providerName, state, code string) (map[string]any, error) {
	oauthState, err := s.loadOAuthState(state)
	if err != nil || oauthState == nil {
		return nil, fmt.Errorf("invalid OAuth state")
	}

	s.mu.RLock()
	p, ok := s.providers[providerName]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown OAuth provider: %s", providerName)
	}

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", p.RedirectURL)
	data.Set("client_id", p.ClientID)
	data.Set("code_verifier", oauthState.CodeVerifier)

	req, err := http.NewRequest(http.MethodPost, p.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tokenPayload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tokenPayload); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: HTTP %d", resp.StatusCode)
	}
	return tokenPayload, nil
}

func (s *OAuthLoginService) GetUserInfo(providerName, accessToken string) (map[string]any, error) {
	s.mu.RLock()
	p, ok := s.providers[providerName]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown OAuth provider: %s", providerName)
	}

	if p.UserInfoURL == "" {
		return nil, nil
	}

	req, err := http.NewRequest(http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var info map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *OAuthLoginService) saveOAuthState(state string, oauthState OAuthState) error {
	states, err := s.loadAllOAuthStates()
	if err != nil {
		return err
	}
	states[state] = oauthState
	data, _ := json.Marshal(states)
	return s.store.SaveJSONDocument("oauth_states.json", json.RawMessage(data))
}

func (s *OAuthLoginService) loadOAuthState(state string) (*OAuthState, error) {
	states, err := s.loadAllOAuthStates()
	if err != nil {
		return nil, err
	}
	val, ok := states[state]
	if !ok {
		return nil, nil
	}
	ss := val.(OAuthState)
	return &ss, nil
}

func (s *OAuthLoginService) loadAllOAuthStates() (map[string]any, error) {
	value, err := s.store.LoadJSONDocument("oauth_states.json")
	if err != nil || value == nil {
		return map[string]any{}, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(typed), &m) == nil {
			return m, nil
		}
	}
	return map[string]any{}, nil
}

func generateOAuthPKCE() (string, string, error) {
	buf := make([]byte, 64)
	_, err := rand.Read(buf)
	if err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func generateOAuthStateToken() (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, 32)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		result[i] = chars[n.Int64()]
	}
	return string(result), nil
}
