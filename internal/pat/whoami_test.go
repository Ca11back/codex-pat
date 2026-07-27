package pat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestNormalizeToken(t *testing.T) {
	token, err := NormalizeToken("  at-fake-token  \n")
	if err != nil {
		t.Fatal(err)
	}
	if token != "at-fake-token" {
		t.Fatalf("token = %q", token)
	}
	for _, raw := range []string{"", "sk-fake", strings.Repeat("a", MaxTokenBytes+1)} {
		if _, err := NormalizeToken(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw[:min(len(raw), 16)])
		}
	}
}

func TestResolveWhoamiURLMatchesCodexBasePathContract(t *testing.T) {
	production, err := ResolveWhoamiURL("")
	if err != nil {
		t.Fatal(err)
	}
	if production != DefaultAuthAPIBaseURL+WhoamiPath {
		t.Fatalf("production whoami URL = %q", production)
	}
	overridden, err := ResolveWhoamiURL("http://127.0.0.1:9000/api/accounts/")
	if err != nil {
		t.Fatal(err)
	}
	if overridden != "http://127.0.0.1:9000/api/accounts"+WhoamiPath {
		t.Fatalf("overridden whoami URL = %q", overridden)
	}
}

func TestFetchIdentityUsesCodexPATContract(t *testing.T) {
	client := httpDoerFunc(func(_ context.Context, callbackID string, request pluginapi.HTTPRequest, limit int) (pluginapi.HTTPResponse, error) {
		if callbackID != "callback-1" {
			t.Fatalf("callback id = %q", callbackID)
		}
		if request.Method != http.MethodGet || request.URL != "http://127.0.0.1:9000/base"+WhoamiPath {
			t.Fatalf("request = %#v", request)
		}
		if limit != MaxWhoamiBodyBytes {
			t.Fatalf("response limit = %d", limit)
		}
		if request.Headers.Get("Authorization") != "Bearer at-fake-secret" {
			t.Fatalf("authorization = %q", request.Headers.Get("Authorization"))
		}
		if request.Headers.Get("Originator") != originator || request.Headers.Get("User-Agent") != "codex-pat/0.1.0" {
			t.Fatalf("headers = %#v", request.Headers)
		}
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body: []byte(`{
                "chatgpt_user_id":"user-1",
                "chatgpt_account_id":"account-1",
                "chatgpt_plan_type":"business",
                "chatgpt_account_is_fedramp":true,
                "extra":"allowed"
            }`),
		}, nil
	})

	identity, err := FetchIdentity(context.Background(), client, "callback-1", "http://127.0.0.1:9000/base/", "0.1.0", "at-fake-secret")
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != "user-1" || identity.AccountID != "account-1" || identity.PlanType != "business" || !identity.FedRAMP || identity.Email != "" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestFetchIdentityClassifiesFailuresWithoutLeakingSecret(t *testing.T) {
	const secret = "at-DO-NOT-LEAK"
	tests := []struct {
		name      string
		response  pluginapi.HTTPResponse
		wantKind  ErrorKind
		retryable bool
	}{
		{name: "unauthorized", response: pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized, Body: []byte(secret)}, wantKind: ErrorAuthentication},
		{name: "rate limited", response: pluginapi.HTTPResponse{StatusCode: http.StatusTooManyRequests}, wantKind: ErrorRateLimited, retryable: true},
		{name: "server", response: pluginapi.HTTPResponse{StatusCode: http.StatusBadGateway}, wantKind: ErrorTransient, retryable: true},
		{name: "missing fedramp", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"chatgpt_user_id":"u","chatgpt_account_id":"a","chatgpt_plan_type":"pro"}`)}, wantKind: ErrorMalformedResponse},
		{name: "trailing", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"chatgpt_user_id":"u","chatgpt_account_id":"a","chatgpt_plan_type":"pro","chatgpt_account_is_fedramp":false}{}`)}, wantKind: ErrorMalformedResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := httpDoerFunc(func(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error) {
				return test.response, nil
			})
			_, err := FetchIdentity(context.Background(), client, "", "", "test", secret)
			var patErr *Error
			if !errors.As(err, &patErr) || patErr.Kind != test.wantKind || patErr.Retryable != test.retryable {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestDecodeIdentityRejectsOversizedAndControlFields(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"chatgpt_user_id":"user\nname","chatgpt_account_id":"a","chatgpt_plan_type":"pro","chatgpt_account_is_fedramp":false}`),
		[]byte(`{"chatgpt_user_id":"u","chatgpt_account_id":"a","chatgpt_plan_type":"` + strings.Repeat("p", MaxIdentityFieldBytes+1) + `","chatgpt_account_is_fedramp":false}`),
	}
	for _, raw := range tests {
		if _, err := decodeIdentity(raw); err == nil {
			t.Fatalf("expected identity to be rejected: %.80s", raw)
		}
	}
}

type httpDoerFunc func(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error)

func (f httpDoerFunc) HTTPDoLimited(ctx context.Context, callbackID string, request pluginapi.HTTPRequest, limit int) (pluginapi.HTTPResponse, error) {
	return f(ctx, callbackID, request, limit)
}
