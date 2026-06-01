package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func registerJSONResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestRegisterFNV1A32MatchesPythonImplementation(t *testing.T) {
	cases := map[string]string{
		"":            "ab3e7c0b",
		"abc":         "1cc93dbc",
		"seedpayload": "769860aa",
		"OpenAI":      "ce220710",
	}
	for input, want := range cases {
		if got := registerFNV1A32(input); got != want {
			t.Fatalf("registerFNV1A32(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestBuildSentinelTokenUsesSentinelChallenge(t *testing.T) {
	worker := &registerWorker{
		deviceID: "device-1",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != registerSentinelBase+"/backend-api/sentinel/req" {
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			if got := req.Header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			return registerJSONResponse(req, http.StatusOK, `{"token":"challenge-token","proofofwork":{"required":false}}`), nil
		})},
	}

	token, err := worker.buildSentinelToken(context.Background(), "username_password_create")
	if err != nil {
		t.Fatalf("buildSentinelToken() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(token), &payload); err != nil {
		t.Fatalf("sentinel token is not JSON: %v", err)
	}
	if payload["c"] != "challenge-token" || payload["id"] != "device-1" || payload["flow"] != "username_password_create" {
		t.Fatalf("sentinel payload = %#v", payload)
	}
	p, _ := payload["p"].(string)
	if !strings.HasPrefix(p, "gAAAAAC") {
		t.Fatalf("sentinel proof token = %q", p)
	}
}

func TestValidateOTPCodeRetriesWithSentinelToken(t *testing.T) {
	validateCalls := 0
	worker := &registerWorker{
		deviceID: "device-1",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/api/accounts/email-otp/validate":
				validateCalls++
				if validateCalls == 1 {
					if req.Header.Get("openai-sentinel-token") != "" {
						t.Fatal("first OTP validate unexpectedly had sentinel token")
					}
					return registerJSONResponse(req, http.StatusForbidden, `{"error":"sentinel_required"}`), nil
				}
				if req.Header.Get("openai-sentinel-token") == "" {
					t.Fatal("second OTP validate did not carry sentinel token")
				}
				return registerJSONResponse(req, http.StatusOK, `{"continue_url":"/continue"}`), nil
			case "/backend-api/sentinel/req":
				return registerJSONResponse(req, http.StatusOK, `{"token":"challenge-token","proofofwork":{"required":false}}`), nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		})},
	}

	payload, err := worker.validateOTPCode(context.Background(), "123456")
	if err != nil {
		t.Fatalf("validateOTPCode() error = %v", err)
	}
	if validateCalls != 2 {
		t.Fatalf("validate calls = %d, want 2", validateCalls)
	}
	if payload["continue_url"] != "/continue" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestExchangeRegistrationTokens(t *testing.T) {
	worker := &registerWorker{
		deviceID:     "device-1",
		codeVerifier: "test-verifier",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/accounts/oauth/token" {
				t.Fatalf("unexpected request path: %s", req.URL.Path)
			}
			if got := req.Header.Get("auth0-client"); got != registerPlatformAuth0Client {
				t.Fatalf("auth0-client header = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["code"] != "auth-code-123" {
				t.Fatalf("body.code = %q", body["code"])
			}
			if body["code_verifier"] != "test-verifier" {
				t.Fatalf("body.code_verifier = %q", body["code_verifier"])
			}
			return registerJSONResponse(req, http.StatusOK, `{"access_token":"access","refresh_token":"refresh","id_token":"id"}`), nil
		})},
	}

	tokens, err := worker.exchangeRegistrationTokens(context.Background(), "auth-code-123")
	if err != nil {
		t.Fatalf("exchangeRegistrationTokens() error = %v", err)
	}
	if tokens["access_token"] != "access" || tokens["refresh_token"] != "refresh" || tokens["id_token"] != "id" {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestRegisterIsCloudflareChallengeDetectsCloudflare(t *testing.T) {
	if !registerIsCloudflareChallenge(map[string]any{"server": "cloudflare"}) {
		t.Fatal("cloudflare server header not detected")
	}
	if !registerIsCloudflareChallenge(map[string]any{"body": "<title>Just a moment"}) {
		t.Fatal("cloudflare challenge page not detected")
	}
	if !registerIsCloudflareChallenge(map[string]any{"body": "challenges.cloudflare.com"}) {
		t.Fatal("cloudflare challenge URL not detected")
	}
	if registerIsCloudflareChallenge(map[string]any{"body": "normal page"}) {
		t.Fatal("normal page falsely detected as cloudflare")
	}
}

func TestRegisterHTTPClientUsesSOCKSTransport(t *testing.T) {
	client, err := registerHTTPClient("socks5h://127.0.0.1:1", time.Second, "device-1")
	if err != nil {
		t.Fatalf("registerHTTPClient() error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("SOCKS register transport should not use http.ProxyURL")
	}
	if transport.DialContext == nil {
		t.Fatal("SOCKS register transport missing DialContext")
	}
}

func TestExtractRegisterMailCodeFromRawMIME(t *testing.T) {
	raw := strings.Join([]string{
		"From: OpenAI <noreply@example.test>",
		"To: user@example.test",
		"Subject: Verify",
		"Content-Type: multipart/alternative; boundary=abc",
		"",
		"--abc",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Your verification code is 654321",
		"--abc--",
	}, "\r\n")
	if got := extractRegisterMailCode(map[string]any{"raw": raw}); got != "654321" {
		t.Fatalf("extractRegisterMailCode(raw) = %q", got)
	}
}

func TestRegisterMessageMatchesEmail(t *testing.T) {
	message := map[string]any{"to": []any{map[string]any{"address": "target@example.test"}}}
	if !registerMessageMatchesEmail(message, "target@example.test") {
		t.Fatal("matching recipient was rejected")
	}
	if registerMessageMatchesEmail(message, "other@example.test") {
		t.Fatal("non-matching recipient was accepted")
	}
}

func TestLatestRegisterMailMessageByTimestamp(t *testing.T) {
	items := []map[string]any{
		{"id": "old", "timestamp": float64(100), "subject": "old"},
		{"id": "new", "timestamp": float64(200), "subject": "new"},
	}
	if got := latestRegisterMailMessage(items); got["id"] != "new" {
		t.Fatalf("latestRegisterMailMessage() = %#v", got)
	}
}
