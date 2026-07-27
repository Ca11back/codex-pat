package management

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddedAssetsAreServedWithStrictHeaders(t *testing.T) {
	t.Parallel()

	for path, contentType := range map[string]string{
		resourceManagePath:      "text/html; charset=utf-8",
		resourceCSSPath:         "text/css; charset=utf-8",
		resourceJSPath:          "text/javascript; charset=utf-8",
		resourceRefreshIconPath: "image/svg+xml",
		resourceTrashIconPath:   "image/svg+xml",
		resourceKeyIconPath:     "image/svg+xml",
	} {
		response, ok := resourceResponse(path)
		if !ok {
			t.Fatalf("resourceResponse(%q) was not found", path)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("resourceResponse(%q) status = %d", path, response.StatusCode)
		}
		if got := response.Headers.Get("Content-Type"); got != contentType {
			t.Fatalf("resourceResponse(%q) content type = %q, want %q", path, got, contentType)
		}
		if got := response.Headers.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("resourceResponse(%q) X-Content-Type-Options = %q", path, got)
		}
		if len(response.Body) == 0 {
			t.Fatalf("resourceResponse(%q) returned an empty body", path)
		}
	}

	html, _ := resourceResponse(resourceManagePath)
	csp := html.Headers.Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self'",
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP %q is missing %q", csp, directive)
		}
	}
	if strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP %q blocks the same-origin CPA management iframe", csp)
	}
}

func TestBrowserAssetsDoNotPersistOrLogSecrets(t *testing.T) {
	t.Parallel()

	for _, path := range []string{resourceManagePath, resourceJSPath} {
		response, ok := resourceResponse(path)
		if !ok {
			t.Fatalf("resourceResponse(%q) was not found", path)
		}
		lower := bytes.ToLower(response.Body)
		for _, forbidden := range [][]byte{
			[]byte("localstorage"),
			[]byte("sessionstorage"),
			[]byte("console.log"),
			[]byte("document.cookie"),
			[]byte("navigator.clipboard"),
			[]byte("access_token"),
			[]byte("chatgpt_user_id"),
		} {
			if bytes.Contains(lower, forbidden) {
				t.Fatalf("resource %q contains forbidden browser API %q", path, forbidden)
			}
		}
	}
}

func TestBrowserAssetsDisplayFullAccountIdentityOnly(t *testing.T) {
	t.Parallel()

	html, _ := resourceResponse(resourceManagePath)
	if !bytes.Contains(html.Body, []byte(`colspan="5"`)) || bytes.Contains(html.Body, []byte(`<th scope="col">Quota</th>`)) {
		t.Fatalf("management HTML does not match the identity-only table contract: %s", html.Body)
	}

	javascript, _ := resourceResponse(resourceJSPath)
	source := string(javascript.Body)
	for _, required := range []string{
		`primary.textContent = account.email`,
		`idLabel.textContent = "Account / Workspace ID"`,
		`idValue.textContent = account.account_id`,
		`fileName.textContent = account.name`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("management JavaScript is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"/v0/management/api-call",
		"backend-api/wham",
		"$TOKEN$",
		"X-OpenAI-Fedramp",
		"redeem_request_id",
		"Reset quota",
		"Check quota",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("identity-only management JavaScript contains discarded quota marker %q", forbidden)
		}
	}
}

func TestUnknownResourceIsNotHandled(t *testing.T) {
	t.Parallel()

	if _, ok := resourceResponse("/v0/resource/plugins/codex-pat/missing"); ok {
		t.Fatal("unknown resource was handled")
	}
}
