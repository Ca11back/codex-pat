package pat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCredentialRoundTripAndRuntimeMapping(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	raw := []byte(`{
        "type":"codex",
        "auth_kind":"pat",
        "access_token":"at-fake-secret",
        "account_id":"workspace-1",
        "chatgpt_user_id":"user-1",
        "email":"user@example.com",
        "plan_type":"enterprise-new",
        "chatgpt_account_is_fedramp":true,
        "validated_at":"2026-07-10T10:00:00Z",
        "headers":{"X-OpenAI-Fedramp":"true"},
        "base_url":"http://127.0.0.1:9999/codex",
        "proxy_url":"http://127.0.0.1:8888",
        "prefix":"work",
        "priority":"7",
        "note":"primary",
        "websockets":false,
        "model_aliases":[{"name":"gpt-test","alias":"test"}]
    }`)
	credential, err := DecodeCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := credential.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"model_aliases"`) || !strings.Contains(string(encoded), `"websockets":false`) {
		t.Fatalf("round trip lost supported fields: %s", encoded)
	}
	auth := credential.AuthData(raw, "codex-pat-test.json", "/tmp/codex-pat-test.json")
	if auth.Provider != Provider || auth.Disabled || auth.Metadata["access_token"] != "at-fake-secret" {
		t.Fatalf("auth = %#v", auth)
	}
	if auth.Attributes["plan_type"] != "enterprise-new" || auth.Attributes["base_url"] != "http://127.0.0.1:9999/codex" || auth.Attributes["priority"] != "7" || auth.Attributes["websockets"] != "false" {
		t.Fatalf("attributes = %#v", auth.Attributes)
	}
	if auth.Attributes["header:X-OpenAI-Fedramp"] != "true" {
		t.Fatalf("FedRAMP attributes = %#v", auth.Attributes)
	}
	if _, exists := auth.Attributes["api_key"]; exists {
		t.Fatal("PAT was mapped as an API key")
	}
	if credential.ValidatedAt != now {
		t.Fatalf("validated at = %v", credential.ValidatedAt)
	}
}

func TestParseAuthDeclinesOAuthAndFailsClosedForMalformedPAT(t *testing.T) {
	oauth := ParseAuth(pluginapi.AuthParseRequest{
		FileName: FileName("oauth-workspace", "oauth-user", "oauth@example.com", "plus"),
		RawJSON:  []byte(`{"type":"codex","access_token":"oauth-token","refresh_token":"refresh"}`),
	})
	if oauth.Handled {
		t.Fatal("normal Codex OAuth file with a PAT-like name was handled")
	}
	explicitOAuth := ParseAuth(pluginapi.AuthParseRequest{
		FileName: FileName("oauth-workspace", "oauth-user", "oauth@example.com", "plus"),
		RawJSON:  []byte(`{"type":"codex","auth_kind":"oauth","access_token":"oauth-token"}`),
	})
	if explicitOAuth.Handled {
		t.Fatal("explicit non-PAT Codex file with a PAT-like name was handled")
	}
	thirdParty := ParseAuth(pluginapi.AuthParseRequest{
		FileName: FileName("third-party", "third-user", "third@example.com", "pro"),
		RawJSON:  []byte(`{"type":"third-party","auth_kind":"pat","access_token":"at-third-party"}`),
	})
	if thirdParty.Handled {
		t.Fatal("non-Codex third-party record with a PAT-like name was handled")
	}

	malformedRaw := []byte(`{"type":"codex","auth_kind":"pat","access_token":"at-secret"}`)
	malformed := ParseAuth(pluginapi.AuthParseRequest{FileName: "codex-pat-bad.json", RawJSON: malformedRaw})
	if !malformed.Handled || !malformed.Auth.Disabled {
		t.Fatalf("malformed response = %#v", malformed)
	}
	if _, exists := malformed.Auth.Metadata["access_token"]; exists {
		t.Fatal("disabled malformed auth exposed token")
	}
	if strings.Contains(malformed.Auth.Label, "at-secret") {
		t.Fatal("disabled label leaked token")
	}

	brokenOwned := ParseAuth(pluginapi.AuthParseRequest{
		FileName: FileName("broken-workspace", "broken-user", "", ""),
		RawJSON:  []byte(`{"type":"codex","auth_kind":123}`),
	})
	if !brokenOwned.Handled || !brokenOwned.Auth.Disabled {
		t.Fatalf("broken owned file response = %#v", brokenOwned)
	}
	brokenOAuth := ParseAuth(pluginapi.AuthParseRequest{
		FileName: "codex-oauth.json",
		RawJSON:  []byte(`{"type":"codex","auth_kind":123}`),
	})
	if brokenOAuth.Handled {
		t.Fatal("broken non-owned OAuth file was handled")
	}
}

func TestCredentialRejectsForbiddenOAuthState(t *testing.T) {
	raw := []byte(`{
        "type":"codex","auth_kind":"pat","access_token":"at-fake",
        "account_id":"a","chatgpt_user_id":"u","plan_type":"pro",
        "chatgpt_account_is_fedramp":false,"validated_at":"2026-07-10T00:00:00Z",
        "refresh_token":"must-not-exist"
    }`)
	credential, err := DecodeCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := credential.Validate(); err == nil {
		t.Fatal("expected forbidden OAuth state to fail validation")
	}
}

func TestRefreshAuthReconstructsPATWithoutReplacingHostFileName(t *testing.T) {
	raw := []byte(`{
        "type":"codex","auth_kind":"pat","access_token":"at-fake",
        "account_id":"a","chatgpt_user_id":"u","plan_type":"pro",
        "chatgpt_account_is_fedramp":false,"validated_at":"2026-07-10T00:00:00Z"
    }`)
	response := RefreshAuth(pluginapi.AuthRefreshRequest{
		AuthID:       "stable-auth-id",
		AuthProvider: Provider,
		StorageJSON:  raw,
	})
	if response.Auth.FileName != "" {
		t.Fatalf("refresh replaced host filename with %q", response.Auth.FileName)
	}
	if response.Auth.Provider != Provider || response.Auth.Metadata["access_token"] != "at-fake" {
		t.Fatalf("refresh auth = %#v", response.Auth)
	}
}

func TestRefreshAuthPassesThroughOrdinaryCodexStorage(t *testing.T) {
	raw := []byte(`{"type":"codex","access_token":"oauth-access","refresh_token":"oauth-refresh"}`)
	response := RefreshAuth(pluginapi.AuthRefreshRequest{
		AuthID:       "oauth-auth-id",
		AuthProvider: Provider,
		StorageJSON:  raw,
		Metadata:     map[string]any{"access_token": "oauth-access"},
		Attributes:   map[string]string{"path": "/tmp/codex-oauth.json"},
	})
	if response.Auth.Disabled || response.Auth.ID != "oauth-auth-id" || response.Auth.FileName != "" {
		t.Fatalf("OAuth passthrough auth = %#v", response.Auth)
	}
	if string(response.Auth.StorageJSON) != string(raw) || response.Auth.Metadata["access_token"] != "oauth-access" {
		t.Fatalf("OAuth passthrough data = %#v", response.Auth)
	}
}

func TestFileNameIsCanonicalDeterministicAndDoesNotExposeAccount(t *testing.T) {
	first := FileName("workspace-sensitive-name", "user-sensitive-id", " User / Name+Tag@Example.COM ", "Team / Plus")
	second := FileName("workspace-sensitive-name", "user-sensitive-id", " User / Name+Tag@Example.COM ", "Team / Plus")
	want := "codex-pat-93bdf0950a663f871f925a7b-user-name+tag@example.com-team-plus.json"
	if first != second || first != want || !IsOwnedFile(first) || strings.Contains(first, "workspace") {
		t.Fatalf("filename = %q, want %q", first, want)
	}
	if other := FileName("workspace-other", "user-sensitive-id", "user@example.com", "team/plus"); other == first {
		t.Fatalf("different workspaces share filename %q", first)
	}
	if other := FileName("workspace-sensitive-name", "other-user", "user@example.com", "team/plus"); other == first {
		t.Fatalf("different users share filename %q", first)
	}
	base := "codex-pat-93bdf0950a663f871f925a7b"
	if got := FileName("workspace-sensitive-name", "user-sensitive-id", "", ""); got != base+".json" {
		t.Fatalf("empty metadata filename = %q", got)
	}
	if got := FileName("workspace-sensitive-name", "user-sensitive-id", "", "Plus"); got != base+"-plus.json" {
		t.Fatalf("plan-only filename = %q", got)
	}
	if got := FileName("workspace-sensitive-name", "user-sensitive-id", "user@example.com", ""); got != base+"-user@example.com.json" {
		t.Fatalf("email-only filename = %q", got)
	}
	bounded := FileName("workspace-sensitive-name", "user-sensitive-id", strings.Repeat("Long Email / ", 30), strings.Repeat("Plan / ", 30))
	maxLength := len(FilePrefix) + identityHashHexLength + 1 + maxEmailFileSegmentBytes + 1 + maxPlanFileSegmentBytes + len(".json")
	if len(bounded) > maxLength || !IsOwnedFile(bounded) {
		t.Fatalf("bounded filename length/grammar = %d %q", len(bounded), bounded)
	}
}

func TestIsOwnedFileUsesStrictCanonicalGrammar(t *testing.T) {
	canonical := FileName("workspace", "user-1", "user@example.com", "business")
	for _, name := range []string{canonical, strings.ToUpper(canonical)} {
		if !IsOwnedFile(name) {
			t.Errorf("IsOwnedFile(%q) = false", name)
		}
	}
	if !IsOwnedFileForPrincipal(canonical, "workspace", "user-1") {
		t.Fatal("canonical filename was not bound to its principal")
	}
	if IsOwnedFileForPrincipal(canonical, "workspace", "different-user") {
		t.Fatal("canonical filename matched a different user")
	}
	if IsOwnedFileForPrincipal(canonical, "different-workspace", "user-1") {
		t.Fatal("canonical filename matched a different workspace")
	}
	retiredAccountOnly := retiredAccountOnlyFileName("workspace")
	if !IsOwnedFile(retiredAccountOnly) {
		t.Fatalf("retired account-only filename %q should share the surface grammar", retiredAccountOnly)
	}
	if IsOwnedFileForPrincipal(retiredAccountOnly, "workspace", "user-1") {
		t.Fatal("retired account-only filename passed principal hash binding")
	}
	for _, name := range []string{
		"codex-pat-broken.json",
		"codex-pat-v2-broken.json",
		"codex-pat-v2-0123456789abcdef01234567.json",
		"codex-pat-0123456789abcdef0123456g.json",
		"codex-pat-0123456789abcdef01234567-.json",
		"codex-pat-0123456789abcdef01234567-user@example.com!.json",
		"codex-user@example.com-plus-pat.json",
		"/tmp/" + canonical,
	} {
		if IsOwnedFile(name) {
			t.Errorf("IsOwnedFile(%q) = true", name)
		}
	}
}

func TestParseAuthFailsClosedWhenOwnedFilenameHashDoesNotMatchPrincipal(t *testing.T) {
	credential := NewCredential("at-fake", Identity{
		UserID:    "user-1",
		AccountID: "account-1",
		PlanType:  "pro",
	}, time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC))
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		FileName("different-account", "user-1", "", ""),
		FileName("account-1", "different-user", "", ""),
		retiredAccountOnlyFileName("account-1"),
	} {
		response := ParseAuth(pluginapi.AuthParseRequest{FileName: name, RawJSON: raw})
		if !response.Handled || !response.Auth.Disabled {
			t.Fatalf("mismatched filename %q response = %#v", name, response)
		}
	}
}

func retiredAccountOnlyFileName(accountID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(accountID)))
	return FilePrefix + hex.EncodeToString(digest[:12]) + ".json"
}
