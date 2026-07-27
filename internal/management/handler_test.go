package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"oaipat/internal/pat"
	"oaipat/internal/plugin"
)

func TestRegisterDeclaresOnlyPlannedManagementAndResourceRoutes(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	registration, err := handler.Register(context.Background(), pluginapi.ManagementRegistrationRequest{})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	wantRoutes := map[string]struct{}{
		http.MethodPost + " " + managementImportPath:     {},
		http.MethodGet + " " + managementStatusPath:      {},
		http.MethodPost + " " + managementRevalidatePath: {},
	}
	if len(registration.Routes) != len(wantRoutes) {
		t.Fatalf("Register() routes = %d, want %d", len(registration.Routes), len(wantRoutes))
	}
	for _, route := range registration.Routes {
		key := route.Method + " " + route.Path
		if _, ok := wantRoutes[key]; !ok {
			t.Errorf("Register() unexpected route %q", key)
		}
		if route.Handler != nil {
			t.Errorf("Register() dynamic route %q unexpectedly has an in-process handler", key)
		}
	}

	menuCount := 0
	paths := make(map[string]struct{}, len(registration.Resources))
	for _, resource := range registration.Resources {
		paths[resource.Path] = struct{}{}
		if resource.Menu != "" {
			menuCount++
			if resource.Path != resourceManagePath || resource.Menu != "Codex PAT" {
				t.Errorf("Register() unexpected menu resource: %+v", resource)
			}
		}
	}
	if menuCount != 1 {
		t.Fatalf("Register() menu resources = %d, want 1", menuCount)
	}
	for path := range assetsByPath {
		if _, ok := paths[path]; !ok {
			t.Errorf("Register() omitted embedded asset %q", path)
		}
	}
}

func TestImportReturnsOnlyValidatedPendingMetadata(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	credential.ChatGPTAccountIsFedRAMP = true
	credential.Headers = map[string]string{"X-OpenAI-Fedramp": "true"}
	credential.BaseURL = "https://auth.internal.invalid/secret-base"
	credential.ProxyURL = "http://proxy-secret.invalid:8080"
	credential.Note = "internal-note-secret"
	fileName := pat.FileName(credential.AccountID, credential.ChatGPTUserID, credential.Email, credential.PlanType)
	service := &fakeLifecycle{importResult: pat.SaveResult{
		Credential: credential,
		FileName:   fileName,
		Path:       "/tmp/" + fileName,
	}}
	handler := &Handler{host: &fakeHost{}, service: service}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodPost,
			Path:   managementImportPath,
			Body:   []byte(`{"pat":"at-test-management-secret"}`),
		},
		HostCallbackID: "callback-123",
	})
	if err != nil {
		t.Fatalf("Handle(import) error = %v", err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("Handle(import) status = %d, want %d: %s", response.StatusCode, http.StatusAccepted, response.Body)
	}
	if service.gotCallbackID != "callback-123" || service.gotToken != "at-test-management-secret" {
		t.Fatalf("Import() received callback=%q token=%q", service.gotCallbackID, service.gotToken)
	}
	if strings.Contains(string(response.Body), "at-test-management-secret") ||
		strings.Contains(string(response.Body), credential.ChatGPTUserID) ||
		strings.Contains(string(response.Body), "X-OpenAI-Fedramp") ||
		strings.Contains(string(response.Body), credential.BaseURL) ||
		strings.Contains(string(response.Body), credential.ProxyURL) ||
		strings.Contains(string(response.Body), credential.Note) ||
		strings.Contains(string(response.Body), "/tmp/") ||
		strings.Contains(strings.ToLower(string(response.Body)), "fedramp") ||
		strings.Contains(string(response.Body), `"headers"`) ||
		strings.Contains(string(response.Body), `"base_url"`) ||
		strings.Contains(string(response.Body), `"proxy_url"`) {
		t.Fatalf("Handle(import) leaked credential internals: %s", response.Body)
	}
	var envelope struct {
		Data accountStatus `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if envelope.Data.Readiness != "pending" {
		t.Fatalf("import readiness = %q, want pending", envelope.Data.Readiness)
	}
	if envelope.Data.Email != credential.Email || envelope.Data.AccountID != credential.AccountID || envelope.Data.Name != fileName {
		t.Fatalf("import identity = %+v", envelope.Data)
	}
}

func TestImportRejectsInvalidAndOversizedJSON(t *testing.T) {
	t.Parallel()

	handler := &Handler{host: &fakeHost{}, service: &fakeLifecycle{}}
	tests := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{name: "unknown field", body: []byte(`{"pat":"at-value","extra":true}`), wantStatus: http.StatusBadRequest},
		{name: "trailing JSON", body: []byte(`{"pat":"at-value"}{}`), wantStatus: http.StatusBadRequest},
		{name: "oversized", body: []byte(strings.Repeat("x", maxManagementBodyBytes+1)), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
				Method: http.MethodPost,
				Path:   managementImportPath,
				Body:   test.body,
			}})
			if err != nil {
				t.Fatalf("Handle(import) error = %v", err)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("Handle(import) status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestImportFailureDoesNotExposeTokenOrUpstreamDetails(t *testing.T) {
	t.Parallel()

	const token = "at-import-failure-secret"
	handler := &Handler{host: &fakeHost{}, service: &fakeLifecycle{importErr: &pat.Error{
		Kind:       pat.ErrorAuthentication,
		Message:    "upstream rejected " + token,
		HTTPStatus: http.StatusUnauthorized,
	}}}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   managementImportPath,
		Body:   []byte(`{"pat":"` + token + `"}`),
	}})
	if err != nil {
		t.Fatalf("Handle(import) error = %v", err)
	}
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("Handle(import) status = %d, want %d", response.StatusCode, http.StatusUnprocessableEntity)
	}
	if strings.Contains(string(response.Body), token) || strings.Contains(string(response.Body), "upstream rejected") {
		t.Fatalf("Handle(import) leaked service details: %s", response.Body)
	}
}

func TestImportResponseRemainsPendingBeforeWatcherReadiness(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	name := pat.FileName(credential.AccountID, credential.ChatGPTUserID, credential.Email, credential.PlanType)
	host := &fakeHost{
		files: []pluginapi.HostAuthFileEntry{{AuthIndex: "generic-index", Name: name, Provider: "codex"}},
		physical: map[string]pluginapi.HostAuthGetResponse{
			"generic-index": {AuthIndex: "generic-index", Name: name, JSON: mustJSON(t, credential)},
		},
		runtime: map[string]pluginapi.HostAuthGetRuntimeResponse{
			"generic-index": {Auth: pluginapi.HostAuthFileEntry{AuthIndex: "generic-index", Name: name, Provider: "codex"}},
		},
	}
	handler := &Handler{host: host, service: &fakeLifecycle{importResult: pat.SaveResult{
		Credential: credential,
		FileName:   name,
	}}}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   managementImportPath,
		Body:   []byte(`{"pat":"at-import-pending"}`),
	}})
	if err != nil {
		t.Fatalf("Handle(import) error = %v", err)
	}
	var envelope struct {
		Data accountStatus `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if envelope.Data.Readiness != "pending" || envelope.Data.AuthIndex != "" {
		t.Fatalf("import response = %+v, want watcher-pending status", envelope.Data)
	}
}

func TestCredentialMutationsAreSerialized(t *testing.T) {
	t.Parallel()

	service := &concurrencyLifecycle{credential: testCredential()}
	handler := &Handler{host: &fakeHost{}, service: service}
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
				Method: http.MethodPost,
				Path:   managementImportPath,
				Body:   []byte(`{"pat":"at-concurrent-test"}`),
			}})
			if err != nil || response.StatusCode != http.StatusAccepted {
				t.Errorf("concurrent import status=%d err=%v", response.StatusCode, err)
			}
		}()
	}
	close(start)
	wait.Wait()
	if got := service.maxConcurrent.Load(); got != 1 {
		t.Fatalf("concurrent credential mutations = %d, want 1", got)
	}
}

func TestStatusFiltersOAuthAndReturnsValidatedPATIdentity(t *testing.T) {
	t.Parallel()

	valid := testCredential()
	valid.ChatGPTAccountIsFedRAMP = true
	valid.Headers = map[string]string{"X-OpenAI-Fedramp": "true"}
	valid.BaseURL = "https://auth.internal.invalid/secret-base"
	valid.ProxyURL = "http://proxy-secret.invalid:8080"
	valid.Note = "internal-note-secret"
	validRaw := mustJSON(t, valid)
	malformedRaw := []byte(`{"type":"codex","auth_kind":"pat","access_token":"at-not-returned"}`)
	oauthRaw := []byte(`{"type":"codex","auth_kind":"oauth","access_token":"oauth-not-returned"}`)
	thirdPartyPATRaw := mustJSON(t, valid)
	validName := pat.FileName(valid.AccountID, valid.ChatGPTUserID, valid.Email, valid.PlanType)
	oauthLikeName := pat.FileName("oauth-workspace", "oauth-user", "oauth@example.com", "plus")
	malformedName := pat.FileName("malformed-workspace", "malformed-user", "", "")
	mismatchedName := pat.FileName(valid.AccountID, "different-user", valid.Email, valid.PlanType)
	retiredAccountOnlyName := retiredAccountOnlyFileName(valid.AccountID)
	retiredVersionName := "codex-pat-v2-0123456789abcdef01234567.json"
	fileModTime := time.Now().Add(-2 * time.Second)
	runtimeUpdatedAt := fileModTime.Add(500 * time.Millisecond)
	host := &fakeHost{
		files: []pluginapi.HostAuthFileEntry{
			{AuthIndex: "oauth-index", Name: "codex-oauth.json", Provider: "codex"},
			{AuthIndex: "marker-oauth", Name: oauthLikeName, Provider: "codex"},
			{AuthIndex: "valid-index", Name: validName, Provider: "codex", ModTime: fileModTime},
			{AuthIndex: "malformed-index", Name: malformedName, Provider: "codex"},
			{AuthIndex: "mismatched-index", Name: mismatchedName, Provider: "codex"},
			{AuthIndex: "retired-account-index", Name: retiredAccountOnlyName, Provider: "codex"},
			{AuthIndex: "retired-version-index", Name: retiredVersionName, Provider: "codex"},
			{AuthIndex: "third-party-pat", Name: "third-party-codex-pat.json", Provider: "codex"},
		},
		physical: map[string]pluginapi.HostAuthGetResponse{
			"marker-oauth":     {AuthIndex: "marker-oauth", Name: oauthLikeName, JSON: oauthRaw},
			"valid-index":      {AuthIndex: "valid-index", Name: validName, JSON: validRaw},
			"malformed-index":  {AuthIndex: "malformed-index", Name: malformedName, JSON: malformedRaw},
			"mismatched-index": {AuthIndex: "mismatched-index", Name: mismatchedName, JSON: validRaw},
			"retired-account-index": {
				AuthIndex: "retired-account-index",
				Name:      retiredAccountOnlyName,
				JSON:      validRaw,
			},
			"retired-version-index": {
				AuthIndex: "retired-version-index",
				Name:      retiredVersionName,
				JSON:      validRaw,
			},
			"third-party-pat": {AuthIndex: "third-party-pat", Name: "third-party-codex-pat.json", JSON: thirdPartyPATRaw},
		},
		runtime: map[string]pluginapi.HostAuthGetRuntimeResponse{
			"valid-index": {Auth: pluginapi.HostAuthFileEntry{AuthIndex: "valid-index", Name: validName, Provider: "codex", UpdatedAt: runtimeUpdatedAt}},
		},
	}
	handler := &Handler{host: host, service: &fakeLifecycle{}}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   managementStatusPath,
	}, HostCallbackID: "status-callback"})
	if err != nil {
		t.Fatalf("Handle(status) error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Handle(status) status = %d: %s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), valid.AccessToken) ||
		strings.Contains(string(response.Body), "oauth-not-returned") ||
		strings.Contains(string(response.Body), valid.ChatGPTUserID) ||
		strings.Contains(string(response.Body), "X-OpenAI-Fedramp") ||
		strings.Contains(string(response.Body), valid.BaseURL) ||
		strings.Contains(string(response.Body), valid.ProxyURL) ||
		strings.Contains(string(response.Body), valid.Note) ||
		strings.Contains(strings.ToLower(string(response.Body)), "fedramp") ||
		strings.Contains(string(response.Body), `"access_token"`) ||
		strings.Contains(string(response.Body), `"chatgpt_user_id"`) ||
		strings.Contains(string(response.Body), `"headers"`) ||
		strings.Contains(string(response.Body), `"base_url"`) ||
		strings.Contains(string(response.Body), `"proxy_url"`) {
		t.Fatalf("Handle(status) leaked credential data: %s", response.Body)
	}
	var envelope struct {
		Data accountList `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if len(envelope.Data.Accounts) != 2 {
		t.Fatalf("status account count = %d, want 2: %+v", len(envelope.Data.Accounts), envelope.Data.Accounts)
	}
	byIndex := make(map[string]accountStatus)
	for _, account := range envelope.Data.Accounts {
		byIndex[account.AuthIndex] = account
	}
	if got := byIndex["valid-index"]; got.Readiness != "ready" || got.Email != valid.Email || got.AccountID != valid.AccountID || got.Name != validName {
		t.Fatalf("valid status = %+v", got)
	}
	if got := byIndex["malformed-index"]; got.Readiness != "disabled" || !got.Disabled || !got.Unavailable {
		t.Fatalf("malformed status = %+v", got)
	}
	if host.lastCallbackID != "status-callback" {
		t.Fatalf("host callback ID = %q, want status-callback", host.lastCallbackID)
	}
}

func TestRevalidateRejectsPATSchemaOutsidePluginFilename(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	for _, test := range []struct {
		name     string
		fileName string
	}{
		{name: "third-party namespace", fileName: "third-party-codex-pat.json"},
		{name: "retired version namespace", fileName: "codex-pat-v2-0123456789abcdef01234567.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{physical: map[string]pluginapi.HostAuthGetResponse{
				"outside-index": {
					AuthIndex: "outside-index",
					Name:      test.fileName,
					JSON:      mustJSON(t, credential),
				},
			}}
			service := &fakeLifecycle{}
			handler := &Handler{host: host, service: service}
			response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
				Method: http.MethodPost,
				Path:   managementRevalidatePath,
				Body:   []byte(`{"auth_index":"outside-index"}`),
			}})
			if err != nil {
				t.Fatalf("Handle(revalidate) error = %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("Handle(revalidate) status = %d, want 404: %s", response.StatusCode, response.Body)
			}
			if service.gotFileName != "" || service.gotCredential.AccessToken != "" {
				t.Fatalf("outside credential reached revalidation: %+v", service.gotCredential)
			}
		})
	}
}

func TestRevalidateRejectsMismatchedPrincipalFilename(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	for _, test := range []struct {
		name     string
		fileName string
	}{
		{name: "different principal", fileName: pat.FileName(credential.AccountID, "different-user", credential.Email, credential.PlanType)},
		{name: "retired account-only hash", fileName: retiredAccountOnlyFileName(credential.AccountID)},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{physical: map[string]pluginapi.HostAuthGetResponse{
				"mismatched-index": {
					AuthIndex: "mismatched-index",
					Name:      test.fileName,
					JSON:      mustJSON(t, credential),
				},
			}}
			service := &fakeLifecycle{}
			handler := &Handler{host: host, service: service}
			response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
				Method: http.MethodPost,
				Path:   managementRevalidatePath,
				Body:   []byte(`{"auth_index":"mismatched-index"}`),
			}})
			if err != nil {
				t.Fatalf("Handle(revalidate) error = %v", err)
			}
			if response.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("Handle(revalidate) status = %d, want 422: %s", response.StatusCode, response.Body)
			}
			if service.gotFileName != "" || service.gotCredential.AccessToken != "" {
				t.Fatalf("mismatched principal reached revalidation: %+v", service.gotCredential)
			}
		})
	}
}

func TestWatcherUpdateSettledRequiresPostWriteRuntimeUpdate(t *testing.T) {
	now := time.Now()
	file := pluginapi.HostAuthFileEntry{ModTime: now.Add(-time.Second)}
	if watcherUpdateSettled(file, pluginapi.HostAuthFileEntry{UpdatedAt: file.ModTime.Add(-time.Millisecond)}, now) {
		t.Fatal("runtime update before file write was reported ready")
	}
	if watcherUpdateSettled(file, pluginapi.HostAuthFileEntry{UpdatedAt: now.Add(-100 * time.Millisecond)}, now) {
		t.Fatal("unsettled watcher update was reported ready")
	}
	if !watcherUpdateSettled(file, pluginapi.HostAuthFileEntry{UpdatedAt: now.Add(-500 * time.Millisecond)}, now) {
		t.Fatal("settled watcher update was not reported ready")
	}
}

func TestStatusHostFailureIsRedactedAndRetryable(t *testing.T) {
	t.Parallel()

	host := &fakeHost{listErr: errors.New("physical path /secret/auth and at-do-not-return")}
	handler := &Handler{host: host, service: &fakeLifecycle{}}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   managementStatusPath,
	}})
	if err != nil {
		t.Fatalf("Handle(status) error = %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Handle(status) status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	if strings.Contains(string(response.Body), "/secret/") || strings.Contains(string(response.Body), "at-do-not-return") {
		t.Fatalf("Handle(status) leaked host error: %s", response.Body)
	}
	if !strings.Contains(string(response.Body), `"retryable":true`) {
		t.Fatalf("Handle(status) response is not retryable: %s", response.Body)
	}
}

func TestRevalidateMapsLifecycleOutcomes(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	name := pat.FileName(credential.AccountID, credential.ChatGPTUserID, credential.Email, credential.PlanType)
	raw := mustJSON(t, credential)
	tests := []struct {
		name       string
		result     pat.RevalidationResult
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "valid",
			result: pat.RevalidationResult{Outcome: pat.RevalidationValid, SaveResult: pat.SaveResult{
				Credential: credential,
				FileName:   name,
			}},
			wantStatus: http.StatusOK,
		},
		{
			name: "invalid disables",
			result: pat.RevalidationResult{Outcome: pat.RevalidationInvalid, SaveResult: pat.SaveResult{
				Credential: credential,
				FileName:   name,
			}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "pat_invalid",
		},
		{
			name: "account mismatch disables",
			result: pat.RevalidationResult{Outcome: pat.RevalidationAccountMismatch, SaveResult: pat.SaveResult{
				Credential: credential,
				FileName:   name,
			}},
			wantStatus: http.StatusConflict,
			wantCode:   "account_mismatch",
		},
		{
			name: "transient unchanged",
			err: &pat.Error{
				Kind:      pat.ErrorTransient,
				Message:   "at-do-not-leak transient details",
				Retryable: true,
			},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   string(pat.ErrorTransient),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeLifecycle{revalidationResult: test.result, revalidationErr: test.err}
			host := &fakeHost{physical: map[string]pluginapi.HostAuthGetResponse{
				"auth-index": {AuthIndex: "auth-index", Name: name, JSON: raw},
			}}
			handler := &Handler{host: host, service: service}
			response, err := handler.Handle(context.Background(), plugin.ManagementRequest{
				ManagementRequest: pluginapi.ManagementRequest{
					Method: http.MethodPost,
					Path:   managementRevalidatePath,
					Body:   []byte(`{"auth_index":"auth-index"}`),
				},
				HostCallbackID: "revalidate-callback",
			})
			if err != nil {
				t.Fatalf("Handle(revalidate) error = %v", err)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("Handle(revalidate) status = %d, want %d: %s", response.StatusCode, test.wantStatus, response.Body)
			}
			if test.wantCode != "" && !strings.Contains(string(response.Body), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("Handle(revalidate) response missing code %q: %s", test.wantCode, response.Body)
			}
			if strings.Contains(string(response.Body), credential.AccessToken) || strings.Contains(string(response.Body), credential.ChatGPTUserID) || strings.Contains(string(response.Body), "at-do-not-leak") {
				t.Fatalf("Handle(revalidate) leaked sensitive data: %s", response.Body)
			}
			if service.gotCallbackID != "revalidate-callback" || service.gotCredential.AccountID != credential.AccountID || service.gotFileName != name {
				t.Fatalf("Revalidate() received callback=%q credential=%+v filename=%q", service.gotCallbackID, service.gotCredential, service.gotFileName)
			}
		})
	}
}

func TestResourceAndUnknownRoutes(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	response, err := handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   resourceManagePath,
	}})
	if err != nil {
		t.Fatalf("Handle(resource) error = %v", err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("Handle(resource) = status %d type %q", response.StatusCode, response.Headers.Get("Content-Type"))
	}

	response, err = handler.Handle(context.Background(), plugin.ManagementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodDelete,
		Path:   managementImportPath,
	}})
	if err != nil {
		t.Fatalf("Handle(unknown) error = %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("Handle(unknown) status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func testCredential() pat.Credential {
	return pat.NewCredential("at-test-secret-never-return", pat.Identity{
		UserID:    "user-123",
		AccountID: "account-1234567890",
		PlanType:  "pro",
		Email:     "alice@example.com",
	}, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func retiredAccountOnlyFileName(accountID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(accountID)))
	return pat.FilePrefix + hex.EncodeToString(digest[:12]) + ".json"
}

type fakeLifecycle struct {
	importResult       pat.SaveResult
	importErr          error
	revalidationResult pat.RevalidationResult
	revalidationErr    error
	gotCallbackID      string
	gotToken           string
	gotCredential      pat.Credential
	gotFileName        string
}

type concurrencyLifecycle struct {
	credential    pat.Credential
	current       atomic.Int32
	maxConcurrent atomic.Int32
}

func (c *concurrencyLifecycle) Import(context.Context, string, string) (pat.SaveResult, error) {
	current := c.current.Add(1)
	for {
		maximum := c.maxConcurrent.Load()
		if current <= maximum || c.maxConcurrent.CompareAndSwap(maximum, current) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	c.current.Add(-1)
	return pat.SaveResult{Credential: c.credential, FileName: pat.FileName(c.credential.AccountID, c.credential.ChatGPTUserID, c.credential.Email, c.credential.PlanType)}, nil
}

func (c *concurrencyLifecycle) Revalidate(context.Context, string, pat.Credential, string) (pat.RevalidationResult, error) {
	return pat.RevalidationResult{}, errors.New("unexpected revalidation")
}

func (f *fakeLifecycle) Import(_ context.Context, callbackID, token string) (pat.SaveResult, error) {
	f.gotCallbackID = callbackID
	f.gotToken = token
	return f.importResult, f.importErr
}

func (f *fakeLifecycle) Revalidate(_ context.Context, callbackID string, credential pat.Credential, fileName string) (pat.RevalidationResult, error) {
	f.gotCallbackID = callbackID
	f.gotCredential = credential
	f.gotFileName = fileName
	return f.revalidationResult, f.revalidationErr
}

type fakeHost struct {
	files          []pluginapi.HostAuthFileEntry
	listErr        error
	physical       map[string]pluginapi.HostAuthGetResponse
	physicalErr    map[string]error
	runtime        map[string]pluginapi.HostAuthGetRuntimeResponse
	runtimeErr     map[string]error
	lastCallbackID string
}

func (f *fakeHost) HTTPDo(context.Context, string, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, errors.New("unexpected HTTPDo")
}

func (f *fakeHost) HTTPDoLimited(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, errors.New("unexpected HTTPDoLimited")
}

func (f *fakeHost) AuthList(_ context.Context, callbackID string) ([]pluginapi.HostAuthFileEntry, error) {
	f.lastCallbackID = callbackID
	return append([]pluginapi.HostAuthFileEntry(nil), f.files...), f.listErr
}

func (f *fakeHost) AuthGet(_ context.Context, callbackID, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	f.lastCallbackID = callbackID
	if err := f.physicalErr[authIndex]; err != nil {
		return pluginapi.HostAuthGetResponse{}, err
	}
	response, ok := f.physical[authIndex]
	if !ok {
		return pluginapi.HostAuthGetResponse{}, errors.New("not found")
	}
	return response, nil
}

func (f *fakeHost) AuthGetRuntime(_ context.Context, callbackID, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	f.lastCallbackID = callbackID
	if err := f.runtimeErr[authIndex]; err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	response, ok := f.runtime[authIndex]
	if !ok {
		return pluginapi.HostAuthGetRuntimeResponse{}, errors.New("not ready")
	}
	return response, nil
}

func (f *fakeHost) AuthSave(context.Context, string, string, json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	return pluginapi.HostAuthSaveResponse{}, errors.New("unexpected AuthSave")
}

func (f *fakeHost) Log(context.Context, string, string, map[string]any) error {
	return nil
}
