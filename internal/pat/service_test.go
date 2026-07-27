package pat

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestServiceImportPersistsSecuredCredential(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("user-1", "account-1", "business", true),
		}},
	}
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	result, err := service.Import(context.Background(), "callback-1", " at-fake-secret ")
	if err != nil {
		t.Fatal(err)
	}
	if host.lastCallbackID != "callback-1" || result.Credential.AccountID != "account-1" || !result.Credential.ChatGPTAccountIsFedRAMP {
		t.Fatalf("result = %#v", result)
	}
	if want := FileName("account-1", "user-1", "user@example.com", "business"); result.FileName != want {
		t.Fatalf("filename = %q, want %q", result.FileName, want)
	}
	assertSecuredCredentialFile(t, result.Path)
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !json.Valid(raw) {
		t.Fatalf("saved JSON = %q", raw)
	}
	var hostPayload map[string]any
	if err := json.Unmarshal(host.lastSavedRaw, &hostPayload); err != nil {
		t.Fatal(err)
	}
	if got := hostPayload["type"]; got != Provider {
		t.Fatalf("interim host upsert type = %#v, want %q", got, Provider)
	}
	if got := hostPayload["auth_kind"]; got != AuthKind {
		t.Fatalf("interim host upsert auth_kind = %#v, want %q", got, AuthKind)
	}
	if got := hostPayload["access_token"]; got != "" {
		t.Fatalf("interim host upsert access_token = %#v, want empty", got)
	}
	if got := hostPayload["disabled"]; got != true {
		t.Fatalf("interim host upsert disabled = %#v, want true", got)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted["access_token"]; got != "at-fake-secret" {
		t.Fatalf("final access_token = %#v, want imported PAT", got)
	}
	if got, exists := persisted["disabled"]; exists && got != false {
		t.Fatalf("final disabled = %#v, want false or omitted", got)
	}
}

func TestServiceImportValidationFailureDoesNotSave(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		response pluginapi.HTTPResponse
	}{
		{name: "wrong prefix", token: "sk-not-a-pat"},
		{name: "unauthorized", token: "at-invalid", response: pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized}},
		{name: "malformed success", token: "at-malformed", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"chatgpt_user_id":"u"}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{dir: t.TempDir(), responses: []pluginapi.HTTPResponse{test.response}}
			service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"))
			if _, err := service.Import(context.Background(), "callback", test.token); err == nil {
				t.Fatal("expected import failure")
			}
			if host.saves != 0 {
				t.Fatalf("saves = %d", host.saves)
			}
		})
	}
}

func TestServiceSaveCredentialWinsOverStaleHostRuntimeRewrite(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 30, 0, 0, time.UTC)
	host := &fakeHost{
		dir: t.TempDir(),
		overwriteAfterSave: []byte(`{
            "type":"codex","auth_kind":"pat","access_token":"at-stale",
            "account_id":"account-1","chatgpt_user_id":"old-user","plan_type":"pro",
            "chatgpt_account_is_fedramp":false,"validated_at":"2026-07-09T00:00:00Z",
            "disabled":false
        }`),
	}
	service := NewService(host, "test", WithClock(func() time.Time { return now }))
	credential := NewCredential("at-current", Identity{UserID: "new-user", AccountID: "account-1", PlanType: "business"}, now)
	credential.Disable(ValidationInvalid, now)
	result, err := service.SaveCredential(context.Background(), "callback", credential)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := DecodeCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "at-current" || !saved.Disabled || saved.ChatGPTUserID != "new-user" {
		t.Fatalf("saved credential = %#v", saved)
	}
	assertSecuredCredentialFile(t, result.Path)
	var hostPayload map[string]any
	if err := json.Unmarshal(host.lastSavedRaw, &hostPayload); err != nil {
		t.Fatal(err)
	}
	if got := hostPayload["access_token"]; got != "" {
		t.Fatalf("disabled host upsert access_token = %#v, want empty", got)
	}
}

func TestServiceWaitsOutStaleWatcherPersistence(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 35, 0, 0, time.UTC)
	base := &fakeHost{dir: t.TempDir()}
	stale := NewCredential("at-stale", Identity{UserID: "old-user", AccountID: "account-1", PlanType: "pro"}, now.Add(-time.Hour))
	staleRaw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	host := &watcherSyncHost{fakeHost: base, staleRaw: staleRaw}
	service := NewService(host, "test",
		WithClock(func() time.Time { return now }),
		withWatcherSyncTiming(time.Second, time.Millisecond, 0),
	)
	current := NewCredential("at-current", Identity{UserID: "new-user", AccountID: "account-1", PlanType: "business"}, now)
	result, err := service.SaveCredential(context.Background(), "callback", current)
	if err != nil {
		t.Fatal(err)
	}
	if host.runtimeCalls < 2 {
		t.Fatalf("runtime checks = %d, want at least 2", host.runtimeCalls)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := DecodeCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != current.AccessToken || saved.PlanType != current.PlanType || saved.Disabled {
		t.Fatalf("saved credential = %#v", saved)
	}
}

func TestCredentialPayloadEqualPreservesJSONNumberPrecision(t *testing.T) {
	if !credentialPayloadEqual([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Fatal("equivalent JSON objects were not equal")
	}
	if credentialPayloadEqual(
		[]byte(`{"priority":9007199254740992}`),
		[]byte(`{"priority":9007199254740993}`),
	) {
		t.Fatal("distinct large JSON numbers compared equal")
	}
}

func TestRewriteSecuredCredentialRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkName := "codex-pat-link.json"
	link := filepath.Join(dir, linkName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := rewriteSecuredCredential(link, linkName, []byte(`{"type":"codex"}`)); err == nil {
		t.Fatal("expected symlink rewrite to be rejected")
	}
}

func TestServiceImportReusesExistingPluginOwnedFilename(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 45, 0, 0, time.UTC)
	oldCredential := NewCredential("at-old", Identity{
		UserID:    "old-user",
		AccountID: "account-1",
		Email:     "old@example.com",
		PlanType:  "pro",
	}, now.Add(-time.Hour))
	oldRaw, err := json.Marshal(oldCredential)
	if err != nil {
		t.Fatal(err)
	}
	fileName := FileName(oldCredential.AccountID, oldCredential.ChatGPTUserID, oldCredential.Email, oldCredential.PlanType)
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("old-user", "account-1", "business", false),
		}},
	}
	host.addAuthFile(fileName, Provider, oldRaw)
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	result, err := service.Import(context.Background(), "callback", "at-new")
	if err != nil {
		t.Fatal(err)
	}
	if result.FileName != fileName || host.lastSavedName != fileName {
		t.Fatalf("existing filename was not preserved: result=%q save=%q", result.FileName, host.lastSavedName)
	}
	if len(host.files) != 1 {
		t.Fatalf("same-principal import created %d files", len(host.files))
	}
}

func TestServiceImportDoesNotReuseRetiredVersionFilename(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 45, 30, 0, time.UTC)
	existing := NewCredential("at-old", Identity{
		UserID:    "user-1",
		AccountID: "account-1",
		Email:     "old@example.com",
		PlanType:  "pro",
	}, now.Add(-time.Hour))
	existingRaw, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	retiredName := FilePrefix + "v2-" + principalHash(existing.AccountID, existing.ChatGPTUserID) + "-old@example.com-pro.json"
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("user-1", "account-1", "business", false),
		}},
	}
	host.addAuthFile(retiredName, Provider, existingRaw)
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	result, err := service.Import(context.Background(), "callback", "at-new")
	if err != nil {
		t.Fatal(err)
	}
	wantName := FileName("account-1", "user-1", "user@example.com", "business")
	if result.FileName != wantName || host.lastSavedName != wantName {
		t.Fatalf("new import filename = %q save=%q, want %q", result.FileName, host.lastSavedName, wantName)
	}
	if got := host.physical[retiredName].JSON; string(got) != string(existingRaw) {
		t.Fatalf("retired filename was rewritten: %s", got)
	}
	if len(host.files) != 2 {
		t.Fatalf("auth file count = %d, want 2", len(host.files))
	}
}

func TestServiceImportRejectsRetiredAccountOnlyFilename(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 45, 45, 0, time.UTC)
	existing := NewCredential("at-old", Identity{
		UserID:    "user-1",
		AccountID: "account-1",
		Email:     "old@example.com",
		PlanType:  "pro",
	}, now.Add(-time.Hour))
	existingRaw, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	retiredName := retiredAccountOnlyFileName(existing.AccountID)
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("user-1", "account-1", "business", false),
		}},
	}
	host.addAuthFile(retiredName, Provider, existingRaw)
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	if _, err := service.Import(context.Background(), "callback", "at-new"); err == nil {
		t.Fatal("expected retired account-only filename to fail principal binding")
	}
	if host.saves != 0 {
		t.Fatalf("retired account-only collision performed %d saves", host.saves)
	}
	if got := host.physical[retiredName].JSON; string(got) != string(existingRaw) {
		t.Fatalf("retired account-only credential changed: %s", got)
	}
}

func TestServiceImportSeparatesUsersWithinOneWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 46, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		email string
	}{
		{name: "empty email", email: ""},
		{name: "shared email", email: "shared@example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			existing := NewCredential("at-user-a", Identity{
				UserID:    "user-a",
				AccountID: "account-1",
				Email:     test.email,
				PlanType:  "pro",
			}, now.Add(-time.Hour))
			existingRaw, err := json.Marshal(existing)
			if err != nil {
				t.Fatal(err)
			}
			existingName := FileName(existing.AccountID, existing.ChatGPTUserID, existing.Email, existing.PlanType)
			host := &fakeHost{
				dir: t.TempDir(),
				responses: []pluginapi.HTTPResponse{{
					StatusCode: http.StatusOK,
					Body:       whoamiBodyWithEmail("user-b", "account-1", test.email, "pro", false),
				}},
			}
			host.addAuthFile(existingName, Provider, existingRaw)
			service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
			result, err := service.Import(context.Background(), "callback", "at-user-b")
			if err != nil {
				t.Fatal(err)
			}
			if result.FileName == existingName || result.FileName != FileName("account-1", "user-b", test.email, "pro") {
				t.Fatalf("different user filename = %q", result.FileName)
			}
			if len(host.files) != 2 {
				t.Fatalf("auth file count = %d, want 2", len(host.files))
			}
			if got := host.physical[existingName].JSON; string(got) != string(existingRaw) {
				t.Fatalf("existing user credential changed: %s", got)
			}
		})
	}
}

func TestServiceImportDoesNotReuseAnotherPluginFilename(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 47, 0, 0, time.UTC)
	otherCredential := NewCredential("at-other-plugin", Identity{
		UserID:    "other-user",
		AccountID: "account-1",
		Email:     "other@example.com",
		PlanType:  "pro",
	}, now.Add(-time.Hour))
	otherRaw, err := json.Marshal(otherCredential)
	if err != nil {
		t.Fatal(err)
	}
	const otherName = "third-party-codex-pat.json"
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("new-user", "account-1", "business", false),
		}},
	}
	host.addAuthFile(otherName, Provider, otherRaw)
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	result, err := service.Import(context.Background(), "callback", "at-new")
	if err != nil {
		t.Fatal(err)
	}
	if result.FileName == otherName || !IsOwnedFile(result.FileName) {
		t.Fatalf("import reused third-party filename %q", result.FileName)
	}
	if got := host.physical[otherName].JSON; string(got) != string(otherRaw) {
		t.Fatalf("third-party credential changed: %s", got)
	}
	if len(host.files) != 2 {
		t.Fatalf("auth file count = %d, want 2", len(host.files))
	}
}

func TestServiceImportFailsClosedOnCanonicalFilenameCollision(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 50, 0, 0, time.UTC)
	targetName := FileName("account-1", "user-1", "user@example.com", "business")
	differentCredential := NewCredential("at-other", Identity{
		UserID:    "other-user",
		AccountID: "different-account",
		Email:     "other@example.com",
		PlanType:  "pro",
	}, now.Add(-time.Hour))
	differentRaw, err := json.Marshal(differentCredential)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		fileName string
		provider string
		raw      []byte
	}{
		{name: "OAuth", provider: Provider, raw: []byte(`{"type":"codex","auth_kind":"oauth","access_token":"oauth"}`)},
		{name: "third party", provider: "third-party", raw: []byte(`{"type":"third-party","auth_kind":"pat","access_token":"third"}`)},
		{name: "malformed non-PAT", provider: Provider, raw: []byte(`{"type":"codex","auth_kind":"oauth","access_token":123}`)},
		{name: "malformed same-principal name", fileName: FileName("account-1", "user-1", "old@example.com", "pro"), provider: Provider, raw: []byte(`{"type":"codex","auth_kind":"pat","access_token":"at-incomplete"}`)},
		{name: "different account PAT", provider: Provider, raw: differentRaw},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fileName := test.fileName
			if fileName == "" {
				fileName = targetName
			}
			host := &fakeHost{
				dir: t.TempDir(),
				responses: []pluginapi.HTTPResponse{{
					StatusCode: http.StatusOK,
					Body:       whoamiBody("user-1", "account-1", "business", false),
				}},
			}
			host.addAuthFile(fileName, test.provider, test.raw)
			service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
			if _, err := service.Import(context.Background(), "callback", "at-new"); err == nil {
				t.Fatal("expected occupied canonical filename to be rejected")
			}
			if host.saves != 0 {
				t.Fatalf("collision performed %d saves", host.saves)
			}
			if got := host.physical[fileName].JSON; string(got) != string(test.raw) {
				t.Fatalf("collision target changed: %s", got)
			}
		})
	}
}

func TestServiceImportFailsClosedOnMultipleFilesForOnePrincipal(t *testing.T) {
	now := time.Date(2026, 7, 10, 11, 55, 0, 0, time.UTC)
	credential := NewCredential("at-old", Identity{UserID: "user-1", AccountID: "account-1", PlanType: "pro"}, now.Add(-time.Hour))
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{
		dir: t.TempDir(),
		responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body:       whoamiBody("user-1", "account-1", "business", false),
		}},
	}
	host.addAuthFile(FileName("account-1", "user-1", "old@example.com", "pro"), Provider, raw)
	host.addAuthFile(FileName("account-1", "user-1", "new@example.com", "business"), Provider, raw)
	service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
	if _, err := service.Import(context.Background(), "callback", "at-new"); err == nil {
		t.Fatal("expected duplicate principal files to fail closed")
	}
	if host.saves != 0 {
		t.Fatalf("duplicate principal import performed %d saves", host.saves)
	}
}

func TestServiceRevalidatePolicies(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	base := NewCredential("at-fake-secret", Identity{UserID: "u", AccountID: "account-1", PlanType: "pro"}, now.Add(-time.Hour))
	tests := []struct {
		name        string
		response    pluginapi.HTTPResponse
		wantOutcome RevalidationOutcome
		wantErr     bool
		wantSaves   int
	}{
		{name: "valid", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: whoamiBody("u", "account-1", "business", false)}, wantOutcome: RevalidationValid, wantSaves: 1},
		{name: "invalid", response: pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized}, wantOutcome: RevalidationInvalid, wantSaves: 1},
		{name: "workspace mismatch", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: whoamiBody("u", "account-2", "pro", false)}, wantOutcome: RevalidationAccountMismatch, wantSaves: 1},
		{name: "user mismatch", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: whoamiBody("u2", "account-1", "pro", false)}, wantOutcome: RevalidationAccountMismatch, wantSaves: 1},
		{name: "transient", response: pluginapi.HTTPResponse{StatusCode: http.StatusServiceUnavailable}, wantErr: true, wantSaves: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{dir: t.TempDir(), responses: []pluginapi.HTTPResponse{test.response}}
			fileName := FileName(base.AccountID, base.ChatGPTUserID, "", base.PlanType)
			baseRaw, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			host.addAuthFile(fileName, Provider, baseRaw)
			service := NewService(host, "test", WithAuthAPIBaseURL("http://auth.invalid"), WithClock(func() time.Time { return now }))
			result, err := service.Revalidate(context.Background(), "callback", base, fileName)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v", err)
			}
			if !test.wantErr && result.Outcome != test.wantOutcome {
				t.Fatalf("outcome = %q", result.Outcome)
			}
			if host.saves != test.wantSaves {
				t.Fatalf("saves = %d", host.saves)
			}
			if !test.wantErr && result.FileName != fileName {
				t.Fatalf("revalidation filename = %q, want %q", result.FileName, fileName)
			}
			if test.wantOutcome == RevalidationInvalid || test.wantOutcome == RevalidationAccountMismatch {
				if !result.Credential.Disabled {
					t.Fatal("definitive failure did not disable credential")
				}
			}
			if test.wantOutcome == RevalidationAccountMismatch && (result.Credential.AccountID != base.AccountID || result.Credential.ChatGPTUserID != base.ChatGPTUserID) {
				t.Fatalf("mismatch rebound principal: %#v", result.Credential)
			}
		})
	}
}

type fakeHost struct {
	dir                string
	responses          []pluginapi.HTTPResponse
	files              []pluginapi.HostAuthFileEntry
	physical           map[string]pluginapi.HostAuthGetResponse
	lastCallbackID     string
	saves              int
	overwriteAfterSave []byte
	lastSavedRaw       []byte
	lastSavedName      string
}

type watcherSyncHost struct {
	*fakeHost
	staleRaw      []byte
	runtimeCalls  int
	staleInjected bool
}

func (h *watcherSyncHost) AuthGetRuntime(_ context.Context, _ string, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	h.runtimeCalls++
	physical, ok := h.physical[authIndex]
	if !ok {
		return pluginapi.HostAuthGetRuntimeResponse{}, os.ErrNotExist
	}
	raw, err := os.ReadFile(physical.Path)
	if err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	var credential map[string]any
	if err := json.Unmarshal(raw, &credential); err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	disabled := credential["access_token"] == ""
	if !disabled && !h.staleInjected {
		if err := os.WriteFile(physical.Path, h.staleRaw, 0o600); err != nil {
			return pluginapi.HostAuthGetRuntimeResponse{}, err
		}
		h.staleInjected = true
	}
	info, err := os.Stat(physical.Path)
	if err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	return pluginapi.HostAuthGetRuntimeResponse{Auth: pluginapi.HostAuthFileEntry{
		AuthIndex: authIndex,
		Name:      physical.Name,
		Provider:  Provider,
		Disabled:  disabled,
		UpdatedAt: info.ModTime().Add(time.Millisecond),
	}}, nil
}

func (h *fakeHost) HTTPDoLimited(_ context.Context, callbackID string, _ pluginapi.HTTPRequest, _ int) (pluginapi.HTTPResponse, error) {
	h.lastCallbackID = callbackID
	if len(h.responses) == 0 {
		return pluginapi.HTTPResponse{}, nil
	}
	response := h.responses[0]
	h.responses = h.responses[1:]
	return response, nil
}

func (h *fakeHost) AuthList(_ context.Context, callbackID string) ([]pluginapi.HostAuthFileEntry, error) {
	h.lastCallbackID = callbackID
	return append([]pluginapi.HostAuthFileEntry(nil), h.files...), nil
}

func (h *fakeHost) AuthGet(_ context.Context, callbackID, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	h.lastCallbackID = callbackID
	response, ok := h.physical[authIndex]
	if !ok {
		return pluginapi.HostAuthGetResponse{}, os.ErrNotExist
	}
	response.JSON = append(json.RawMessage(nil), response.JSON...)
	return response, nil
}

func (h *fakeHost) AuthSave(_ context.Context, callbackID, name string, raw json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	h.lastCallbackID = callbackID
	h.saves++
	h.lastSavedRaw = append([]byte(nil), raw...)
	h.lastSavedName = name
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return pluginapi.HostAuthSaveResponse{}, err
	}
	if len(h.overwriteAfterSave) > 0 {
		if err := os.WriteFile(path, h.overwriteAfterSave, 0o644); err != nil {
			return pluginapi.HostAuthSaveResponse{}, err
		}
	}
	h.addAuthFile(name, Provider, raw)
	return pluginapi.HostAuthSaveResponse{Name: name, Path: path}, nil
}

func (h *fakeHost) addAuthFile(name, provider string, raw []byte) {
	if h.physical == nil {
		h.physical = make(map[string]pluginapi.HostAuthGetResponse)
	}
	authIndex := name
	path := filepath.Join(h.dir, name)
	h.physical[authIndex] = pluginapi.HostAuthGetResponse{
		AuthIndex: authIndex,
		Name:      name,
		Path:      path,
		JSON:      append(json.RawMessage(nil), raw...),
	}
	for index := range h.files {
		if h.files[index].AuthIndex == authIndex {
			h.files[index].Name = name
			h.files[index].Provider = provider
			h.files[index].Type = provider
			return
		}
	}
	h.files = append(h.files, pluginapi.HostAuthFileEntry{
		AuthIndex: authIndex,
		Name:      name,
		Provider:  provider,
		Type:      provider,
		Path:      path,
	})
}

func whoamiBody(user, account, plan string, fedramp bool) []byte {
	return whoamiBodyWithEmail(user, account, "user@example.com", plan, fedramp)
}

func whoamiBodyWithEmail(user, account, email, plan string, fedramp bool) []byte {
	raw, _ := json.Marshal(map[string]any{
		"chatgpt_user_id":            user,
		"chatgpt_account_id":         account,
		"chatgpt_plan_type":          plan,
		"chatgpt_account_is_fedramp": fedramp,
		"email":                      email,
	})
	return raw
}
