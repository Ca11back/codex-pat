//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	patlogic "oaipat/internal/pat"
)

const (
	defaultPluginVersion = "0.1.6"
	upgradeFromVersion   = "0.1.5"
	verifiedCPAVersion   = "v7.2.129"
	managementKey        = "codex-pat-integration-management-key"
	clientAPIKey         = "codex-pat-integration-client-key"
	whoamiPath           = "/api/accounts/v1/user-auth-credential/whoami"
)

type mockReply struct {
	status int
	body   string
}

type observedRequest struct {
	method string
	uri    string
	header http.Header
	body   []byte
}

type mockBackend struct {
	server *httptest.Server

	mu             sync.Mutex
	whoamiReplies  map[string]mockReply
	whoamiRequests []observedRequest
	codexRequests  []observedRequest
	oauthRequests  []observedRequest
}

func newMockBackend(t *testing.T) *mockBackend {
	t.Helper()

	backend := &mockBackend{whoamiReplies: make(map[string]mockReply)}
	backend.server = httptest.NewServer(http.HandlerFunc(backend.serveHTTP))
	t.Cleanup(backend.server.Close)
	return backend
}

func (b *mockBackend) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	request := observedRequest{
		method: r.Method,
		uri:    r.URL.RequestURI(),
		header: r.Header.Clone(),
		body:   bytes.Clone(body),
	}

	switch r.URL.Path {
	case whoamiPath:
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b.mu.Lock()
		b.whoamiRequests = append(b.whoamiRequests, request)
		reply, ok := b.whoamiReplies[token]
		b.mu.Unlock()
		if !ok {
			reply = mockReply{status: http.StatusUnauthorized, body: `{"error":"unknown fake credential"}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	case "/oauth/token":
		b.mu.Lock()
		b.oauthRequests = append(b.oauthRequests, request)
		b.mu.Unlock()
		http.Error(w, "OAuth must not be called for PAT auth", http.StatusInternalServerError)
	default:
		if strings.HasSuffix(r.URL.Path, "/responses") || r.URL.Path == "/responses" {
			b.mu.Lock()
			b.codexRequests = append(b.codexRequests, request)
			b.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"local mock rejection","type":"invalid_request_error"}}`)
			return
		}
		http.NotFound(w, r)
	}
}

func (b *mockBackend) setWhoami(token string, status int, body string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.whoamiReplies[token] = mockReply{status: status, body: body}
}

func (b *mockBackend) counts() (whoami, codex, oauth int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.whoamiRequests), len(b.codexRequests), len(b.oauthRequests)
}

func (b *mockBackend) lastWhoami(t *testing.T) observedRequest {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.whoamiRequests) == 0 {
		t.Fatal("mock Auth API received no whoami request")
	}
	return b.whoamiRequests[len(b.whoamiRequests)-1]
}

func (b *mockBackend) waitForCodexRequest(t *testing.T) observedRequest {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		if len(b.codexRequests) > 0 {
			request := b.codexRequests[len(b.codexRequests)-1]
			b.mu.Unlock()
			return request
		}
		b.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("CPA native Codex executor did not reach the local mock")
	return observedRequest{}
}

type cpaProcess struct {
	cmd     *exec.Cmd
	done    chan error
	logFile *os.File
	logPath string
}

func startCPA(t *testing.T, binary, workDir, configPath, authAPIBase string, port int) *cpaProcess {
	t.Helper()

	logPath := filepath.Join(workDir, "cpa.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open CPA log: %v", err)
	}

	cmd := exec.Command(binary, "-config", configPath, "-local-model")
	cmd.Dir = workDir
	cmd.Env = environmentWithOverrides(os.Environ(), []string{
		"CODEX_AUTHAPI_BASE_URL=" + authAPIBase,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"http_proxy=http://127.0.0.1:1",
		"https_proxy=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if errStart := cmd.Start(); errStart != nil {
		_ = logFile.Close()
		t.Fatalf("start CPA: %v", errStart)
	}

	process := &cpaProcess{
		cmd:     cmd,
		done:    make(chan error, 1),
		logFile: logFile,
		logPath: logPath,
	}
	go func() {
		process.done <- cmd.Wait()
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case errWait := <-process.done:
			_ = logFile.Close()
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("CPA exited before readiness: %v\n%s", errWait, logs)
		default:
		}

		response, errGet := http.Get(baseURL + "/healthz")
		if errGet == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return process
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	process.stop(t)
	logs, _ := os.ReadFile(logPath)
	t.Fatalf("CPA did not become ready\n%s", logs)
	return nil
}

func (p *cpaProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}

	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			t.Error("CPA process did not stop after kill")
		}
	}
	_ = p.logFile.Close()
	p.cmd = nil
}

type smokeEnvironment struct {
	root                  string
	tempDir               string
	authDir               string
	pluginDir             string
	configPath            string
	pluginPath            string
	pluginSourcePath      string
	pluginVersion         string
	previousPluginPath    string
	previousPluginVersion string
	cpaBinary             string
	port                  int
	baseURL               string
}

func prepareEnvironment(t *testing.T) smokeEnvironment {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the required CPA integration target is linux/amd64")
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	root := filepath.Dir(filepath.Dir(currentFile))
	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	pluginDir := filepath.Join(tempDir, "plugins")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("create plugin directory: %v", err)
	}

	pluginVersion := strings.TrimSpace(os.Getenv("CODEX_PAT_VERSION"))
	if pluginVersion == "" {
		pluginVersion = defaultPluginVersion
	}
	pluginPath := filepath.Join(pluginDir, "codex-pat-v"+pluginVersion+".so")
	pluginSourcePath := filepath.Join(tempDir, filepath.Base(pluginPath))
	if supplied := strings.TrimSpace(os.Getenv("CODEX_PAT_PLUGIN")); supplied != "" {
		if !filepath.IsAbs(supplied) {
			supplied = filepath.Join(root, supplied)
		}
		copyFile(t, supplied, pluginSourcePath, 0o755)
	} else {
		runCommand(t, root, []string{"CGO_ENABLED=1"}, "go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared", "-ldflags", "-s -w -X main.version="+pluginVersion, "-o", pluginSourcePath, "./cmd/codex-pat")
	}
	previousPluginPath := filepath.Join(pluginDir, "codex-pat-v"+upgradeFromVersion+".so")
	runCommand(t, root, []string{"CGO_ENABLED=1"}, "go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared", "-ldflags", "-s -w -X main.version="+upgradeFromVersion, "-o", previousPluginPath, "./cmd/codex-pat")

	cpaBinary := strings.TrimSpace(os.Getenv("CPA_BIN"))
	if cpaBinary == "" {
		cpaBinary = filepath.Join(tempDir, "cli-proxy-api")
		cpaSource := strings.TrimSpace(os.Getenv("CPA_SOURCE"))
		if cpaSource == "" {
			cpaSource = filepath.Join(filepath.Dir(root), "CLIProxyAPI")
		} else if !filepath.IsAbs(cpaSource) {
			cpaSource = filepath.Join(root, cpaSource)
		}
		verifyCPASource(t, cpaSource)
		runCommand(t, cpaSource, []string{"CGO_ENABLED=1"}, "go", "build", "-trimpath", "-o", cpaBinary, "./cmd/server")
	} else if !filepath.IsAbs(cpaBinary) {
		cpaBinary = filepath.Join(root, cpaBinary)
	}

	port := freePort(t)
	return smokeEnvironment{
		root:                  root,
		tempDir:               tempDir,
		authDir:               authDir,
		pluginDir:             pluginDir,
		configPath:            filepath.Join(tempDir, "config.yaml"),
		pluginPath:            pluginPath,
		pluginSourcePath:      pluginSourcePath,
		pluginVersion:         pluginVersion,
		previousPluginPath:    previousPluginPath,
		previousPluginVersion: upgradeFromVersion,
		cpaBinary:             cpaBinary,
		port:                  port,
		baseURL:               fmt.Sprintf("http://127.0.0.1:%d", port),
	}
}

func verifyCPASource(t *testing.T, source string) {
	t.Helper()
	output := runCommandOutput(t, source, nil, "git", "describe", "--tags", "--exact-match", "HEAD")
	if version := strings.TrimSpace(string(output)); version != verifiedCPAVersion {
		t.Fatalf("CPA source must be %s, got %q", verifiedCPAVersion, version)
	}
	output = runCommandOutput(t, source, nil, "git", "status", "--porcelain")
	if changes := strings.TrimSpace(string(output)); changes != "" {
		t.Fatalf("CPA source must be a clean %s checkout; local changes:\n%s", verifiedCPAVersion, changes)
	}
}

func runCommand(t *testing.T, dir string, extraEnv []string, name string, args ...string) {
	t.Helper()
	_ = runCommandOutput(t, dir, extraEnv, name, args...)
}

func runCommandOutput(t *testing.T, dir string, extraEnv []string, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = environmentWithOverrides(os.Environ(), extraEnv)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return output
}

func environmentWithOverrides(base, overrides []string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for _, item := range overrides {
		if key, _, ok := strings.Cut(item, "="); ok {
			keys[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replaced := keys[key]; replaced {
				continue
			}
		}
		out = append(out, item)
	}
	return append(out, overrides...)
}

func copyFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read supplied artifact %s: %v", source, err)
	}
	if errWrite := os.WriteFile(destination, data, mode); errWrite != nil {
		t.Fatalf("copy supplied artifact to %s: %v", destination, errWrite)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate CPA port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func writeCPAConfig(t *testing.T, env smokeEnvironment, pluginEnabled bool) {
	t.Helper()
	config := fmt.Sprintf(`host: "127.0.0.1"
port: %d
remote-management:
  allow-remote: true
  secret-key: %s
  disable-control-panel: true
  disable-auto-update-panel: true
auth-dir: %s
api-keys:
  - %s
debug: false
logging-to-file: false
request-retry: 0
max-retry-credentials: 0
plugins:
  enabled: true
  dir: %s
  configs:
    codex-pat:
      enabled: %t
      store:
        version: %s
`, env.port, strconv.Quote(managementKey), strconv.Quote(env.authDir), strconv.Quote(clientAPIKey), strconv.Quote(env.pluginDir), pluginEnabled, strconv.Quote(env.pluginVersion))
	if err := os.WriteFile(env.configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write CPA config: %v", err)
	}
}

func doRequest(t *testing.T, client *http.Client, method, url, key string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("create %s %s request: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-Management-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, url, err)
	}
	return resp.StatusCode, responseBody
}

func loopbackClient(t *testing.T, octet int) *http.Client {
	t.Helper()
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			LocalAddr: &net.TCPAddr{IP: net.ParseIP(fmt.Sprintf("127.0.0.%d", octet))},
			Timeout:   5 * time.Second,
		}).DialContext,
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func waitForPlugin(t *testing.T, client *http.Client, baseURL, logPath, pluginVersion string, effective bool) []byte {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		status, body := doRequest(t, client, http.MethodGet, baseURL+"/v0/management/plugins", managementKey, nil)
		lastBody = body
		if status == http.StatusOK {
			var response struct {
				Plugins []struct {
					ID               string `json:"id"`
					Registered       bool   `json:"registered"`
					EffectiveEnabled bool   `json:"effective_enabled"`
					Menus            []struct {
						Path string `json:"path"`
						Menu string `json:"menu"`
					} `json:"menus"`
					Metadata *struct {
						Name             string `json:"name"`
						Version          string `json:"version"`
						Author           string `json:"author"`
						GitHubRepository string `json:"github_repository"`
					} `json:"metadata"`
				} `json:"plugins"`
			}
			if json.Unmarshal(body, &response) == nil {
				for _, plugin := range response.Plugins {
					if plugin.ID != "codex-pat" || plugin.Registered != effective || plugin.EffectiveEnabled != effective {
						continue
					}
					if effective {
						if plugin.Metadata == nil || plugin.Metadata.Name != "Codex PAT" || plugin.Metadata.Version != pluginVersion || plugin.Metadata.Author == "" || plugin.Metadata.GitHubRepository == "" {
							continue
						}
						if len(plugin.Menus) != 1 || plugin.Menus[0].Menu != "Codex PAT" || plugin.Menus[0].Path != "/v0/resource/plugins/codex-pat/manage" {
							continue
						}
					}
					return body
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := os.ReadFile(logPath)
	t.Fatalf("codex-pat effective=%t was not observed; last response=%s\nCPA logs:\n%s", effective, lastBody, logs)
	return nil
}

func waitForFileRemoval(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("stat %s while waiting for removal: %v", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("unselected plugin artifact was not removed: %s", path)
}

func waitForPATFile(t *testing.T, authDir string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(authDir, "codex-pat-*.json"))
		sort.Strings(matches)
		if len(matches) == 1 {
			return matches[0]
		}
		if len(matches) > 1 {
			t.Fatalf("expected one PAT auth file, got %v", matches)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("plugin did not create a codex-pat auth file")
	return ""
}

func waitForPATFileCount(t *testing.T, authDir string, count int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(authDir, "codex-pat-*.json"))
		sort.Strings(matches)
		if len(matches) == count {
			return matches
		}
		time.Sleep(50 * time.Millisecond)
	}
	matches, _ := filepath.Glob(filepath.Join(authDir, "codex-pat-*.json"))
	t.Fatalf("PAT auth file count = %d, want %d; files=%v", len(matches), count, matches)
	return nil
}

func waitForAuthIndex(t *testing.T, client *http.Client, baseURL, name string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		status, body := doRequest(t, client, http.MethodGet, baseURL+"/v0/management/auth-files", managementKey, nil)
		lastBody = body
		if status == http.StatusOK {
			var response struct {
				Files []struct {
					Name      string `json:"name"`
					AuthIndex string `json:"auth_index"`
				} `json:"files"`
			}
			if json.Unmarshal(body, &response) == nil {
				for _, file := range response.Files {
					if file.Name == name && file.AuthIndex != "" {
						return file.AuthIndex
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("auth index for %s was not registered; last response=%s", name, lastBody)
	return ""
}

func waitForAuthIndexRemoval(t *testing.T, client *http.Client, baseURL, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		status, body := doRequest(t, client, http.MethodGet, baseURL+"/v0/management/auth-files", managementKey, nil)
		lastBody = body
		if status == http.StatusOK {
			var response struct {
				Files []struct {
					Name string `json:"name"`
				} `json:"files"`
			}
			if json.Unmarshal(body, &response) == nil {
				found := false
				for _, file := range response.Files {
					if file.Name == name {
						found = true
						break
					}
				}
				if !found {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("auth index for %s remained registered; last response=%s", name, lastBody)
}

func waitForModel(t *testing.T, client *http.Client, baseURL, name string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	url := baseURL + "/v0/management/auth-files/models?name=" + name
	var lastBody []byte
	for time.Now().Before(deadline) {
		status, body := doRequest(t, client, http.MethodGet, url, managementKey, nil)
		lastBody = body
		if status == http.StatusOK {
			var response struct {
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
			}
			if json.Unmarshal(body, &response) == nil && len(response.Models) > 0 && response.Models[0].ID != "" {
				return response.Models[0].ID
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no registered model for %s; last response=%s", name, lastBody)
	return ""
}

func waitForPluginReadiness(t *testing.T, client *http.Client, baseURL, logPath, name, readiness string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		status, body := doRequest(t, client, http.MethodGet, baseURL+"/v0/management/plugins/codex-pat/status", managementKey, nil)
		lastBody = body
		if status == http.StatusOK {
			var response struct {
				Data struct {
					Accounts []struct {
						Name      string `json:"name"`
						Readiness string `json:"readiness"`
					} `json:"accounts"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &response) == nil {
				for _, account := range response.Data.Accounts {
					if account.Name == name && account.Readiness == readiness {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := os.ReadFile(logPath)
	t.Fatalf("plugin status for %s did not reach %s; last response=%s\nCPA logs:\n%s", name, readiness, lastBody, logs)
}

func readJSONObject(t *testing.T, path string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
		} else {
			var object map[string]any
			if errDecode := json.Unmarshal(data, &object); errDecode == nil && object != nil {
				return object
			} else {
				lastErr = errDecode
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("read stable JSON object from %s: %v", path, lastErr)
	return nil
}

func waitForCredentialField(t *testing.T, path, key string, want any) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = readJSONObject(t, path)
		if last[key] == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("persisted %s = %#v, want %#v; redacted state=%s", key, last[key], want, redactedCredentialState(last))
	return nil
}

func redactedCredentialState(credential map[string]any) string {
	state := map[string]any{
		"type":             credential["type"],
		"auth_kind":        credential["auth_kind"],
		"plan_type":        credential["plan_type"],
		"disabled":         credential["disabled"],
		"validation_state": credential["validation_state"],
	}
	token, tokenExists := credential["access_token"]
	state["access_token_present"] = tokenExists
	state["access_token_empty"] = tokenExists && token == ""
	raw, _ := json.Marshal(state)
	return string(raw)
}

func assertNoSecret(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(body, []byte(secret)) {
			t.Fatalf("response or log exposed a fake PAT: %s", body)
		}
	}
}

func validWhoami(userID, accountID, email, plan string, fedramp bool) string {
	body, _ := json.Marshal(map[string]any{
		"chatgpt_user_id":            userID,
		"chatgpt_account_id":         accountID,
		"chatgpt_plan_type":          plan,
		"chatgpt_account_is_fedramp": fedramp,
		"email":                      email,
	})
	return string(body)
}

func fakePAT(label string) string {
	return "at-" + strings.Repeat(label+"-", 4) + "not-a-real-credential"
}

func TestCPACodexPATBlackBoxSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("CPA process integration is disabled by -short")
	}

	env := prepareEnvironment(t)
	backend := newMockBackend(t)
	authAPIBase := backend.server.URL + "/api/accounts"
	client := &http.Client{Timeout: 10 * time.Second}

	validPAT := fakePAT("valid")
	invalidPAT := fakePAT("invalid")
	malformedPAT := fakePAT("malformed")
	incompletePAT := fakePAT("incomplete")
	transientPAT := fakePAT("transient")
	replacementPAT := fakePAT("replacement")
	principalReplacementPAT := fakePAT("principal-replacement")
	sameWorkspaceOtherUserPAT := fakePAT("same-workspace-other-user")
	secondWorkspacePAT := fakePAT("second-workspace")
	allSecrets := []string{validPAT, invalidPAT, malformedPAT, incompletePAT, transientPAT, replacementPAT, principalReplacementPAT, sameWorkspaceOtherUserPAT, secondWorkspacePAT}
	currentPAT := validPAT

	backend.setWhoami(validPAT, http.StatusOK, validWhoami("user-integration", "workspace-integration", "integration@example.invalid", "pro", true))
	backend.setWhoami(invalidPAT, http.StatusUnauthorized, `{"error":"invalid fake PAT"}`)
	backend.setWhoami(malformedPAT, http.StatusOK, `{"chatgpt_user_id":`)
	backend.setWhoami(incompletePAT, http.StatusOK, `{"chatgpt_user_id":"user-integration","chatgpt_plan_type":"pro","chatgpt_account_is_fedramp":false}`)
	backend.setWhoami(transientPAT, http.StatusServiceUnavailable, `{"error":"temporary fake failure"}`)
	backend.setWhoami(replacementPAT, http.StatusOK, validWhoami("user-integration", "workspace-integration", "integration@example.invalid", "pro", true))
	backend.setWhoami(principalReplacementPAT, http.StatusOK, validWhoami("user-integration", "workspace-integration", "integration@example.invalid", "business", true))
	backend.setWhoami(sameWorkspaceOtherUserPAT, http.StatusOK, validWhoami("user-other", "workspace-integration", "integration@example.invalid", "pro", true))
	backend.setWhoami(secondWorkspacePAT, http.StatusOK, validWhoami("user-integration", "workspace-second", "integration@example.invalid", "plus", false))

	oauthFixture := []byte(`{"type":"codex","access_token":"fake-oauth-access","id_token":"fake-oauth-id","account_id":"oauth-workspace","disabled":true}`)
	oauthPath := filepath.Join(env.authDir, "codex-oauth-fixture.json")
	if err := os.WriteFile(oauthPath, oauthFixture, 0o600); err != nil {
		t.Fatalf("write OAuth isolation fixture: %v", err)
	}

	if env.pluginVersion == env.previousPluginVersion {
		t.Fatalf("integration release version %s must differ from upgrade baseline %s", env.pluginVersion, env.previousPluginVersion)
	}
	previousEnv := env
	previousEnv.pluginVersion = env.previousPluginVersion
	writeCPAConfig(t, previousEnv, true)
	process := startCPA(t, env.cpaBinary, env.tempDir, env.configPath, authAPIBase, env.port)
	t.Cleanup(func() { process.stop(t) })
	waitForPlugin(t, client, env.baseURL, process.logPath, env.previousPluginVersion, true)
	process.stop(t)
	if _, err := os.Stat(env.previousPluginPath); err != nil {
		t.Fatalf("previous plugin artifact was not available before upgrade: %v", err)
	}
	copyFile(t, env.pluginSourcePath, env.pluginPath, 0o755)
	writeCPAConfig(t, env, true)
	process = startCPA(t, env.cpaBinary, env.tempDir, env.configPath, authAPIBase, env.port)
	waitForPlugin(t, client, env.baseURL, process.logPath, env.pluginVersion, true)
	waitForFileRemoval(t, env.previousPluginPath)
	if _, err := os.Stat(env.pluginPath); err != nil {
		t.Fatalf("selected release plugin artifact is unavailable after upgrade: %v", err)
	}

	status, body := doRequest(t, client, http.MethodGet, env.baseURL+"/v0/management/plugins", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated plugin list status = %d, want 401; body=%s", status, body)
	}
	for index, request := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/v0/management/plugins/codex-pat/status"},
		{method: http.MethodPost, path: "/v0/management/plugins/codex-pat/revalidate", body: map[string]string{"auth_index": "not-present"}},
		{method: http.MethodDelete, path: "/v0/management/auth-files?name=codex-pat-not-present.json"},
	} {
		status, body = doRequest(t, loopbackClient(t, index+2), request.method, env.baseURL+request.path, "", request.body)
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s status = %d, want 401; body=%s", request.method, request.path, status, body)
		}
		assertNoSecret(t, body, allSecrets...)
	}

	resourceRequest, errResourceRequest := http.NewRequest(http.MethodGet, env.baseURL+"/v0/resource/plugins/codex-pat/manage", nil)
	if errResourceRequest != nil {
		t.Fatalf("create resource page request: %v", errResourceRequest)
	}
	resourceResponse, errResource := client.Do(resourceRequest)
	if errResource != nil {
		t.Fatalf("get resource page: %v", errResource)
	}
	body, errResourceBody := io.ReadAll(resourceResponse.Body)
	resourceResponse.Body.Close()
	if errResourceBody != nil {
		t.Fatalf("read resource page: %v", errResourceBody)
	}
	status = resourceResponse.StatusCode
	if status != http.StatusOK || !bytes.Contains(body, []byte("Codex PAT")) {
		t.Fatalf("resource page status/body = %d %s", status, body)
	}
	csp := resourceResponse.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") || strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("resource page CSP blocks CPA iframe: %q", csp)
	}
	for _, assetPath := range []string{
		"/v0/resource/plugins/codex-pat/assets/app.css",
		"/v0/resource/plugins/codex-pat/assets/app.js",
		"/v0/resource/plugins/codex-pat/assets/icons/key-round.svg",
		"/v0/resource/plugins/codex-pat/assets/icons/refresh-cw.svg",
		"/v0/resource/plugins/codex-pat/assets/icons/trash-2.svg",
	} {
		status, body = doRequest(t, client, http.MethodGet, env.baseURL+assetPath, "", nil)
		if status != http.StatusOK || len(body) == 0 {
			t.Fatalf("resource asset %s status/body = %d %q", assetPath, status, body)
		}
	}

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", "", map[string]string{"pat": validPAT})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated import status = %d, want 401; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)

	for _, nonPAT := range []string{"", "sk-not-a-codex-pat"} {
		whoamiBefore, _, _ := backend.counts()
		status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": nonPAT})
		if status < 400 || status >= 500 {
			t.Fatalf("non-PAT import status = %d, want a client error; body=%s", status, body)
		}
		whoamiAfter, _, _ := backend.counts()
		if whoamiAfter != whoamiBefore {
			t.Fatal("non-PAT input reached whoami")
		}
	}

	for _, rejected := range []string{invalidPAT, malformedPAT, incompletePAT, transientPAT} {
		status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": rejected})
		if status < 400 {
			t.Fatalf("rejected fake PAT import status = %d; body=%s", status, body)
		}
		assertNoSecret(t, body, allSecrets...)
		matches, _ := filepath.Glob(filepath.Join(env.authDir, "codex-pat-*.json"))
		if len(matches) != 0 {
			t.Fatalf("rejected import persisted PAT files: %v", matches)
		}
	}

	canonicalCollisionName := patlogic.FileName("workspace-integration", "user-integration", "integration@example.invalid", "pro")
	differentAccountPAT := fakePAT("different-account")
	malformedCollisionPAT := "at-incomplete"
	allSecrets = append(allSecrets, differentAccountPAT, malformedCollisionPAT)
	differentAccountCredential := patlogic.NewCredential(differentAccountPAT, patlogic.Identity{
		UserID:    "user-different-account",
		AccountID: "workspace-different-account",
		Email:     "different@example.invalid",
		PlanType:  "pro",
	}, time.Now().UTC())
	differentAccountRaw, err := json.Marshal(differentAccountCredential)
	if err != nil {
		t.Fatalf("encode different-account collision: %v", err)
	}
	for _, collision := range []struct {
		name string
		raw  []byte
	}{
		{name: "OAuth", raw: []byte(`{"type":"codex","auth_kind":"oauth","access_token":"oauth-collision"}`)},
		{name: "third party", raw: []byte(`{"type":"third-party","auth_kind":"pat","access_token":"third-party-collision"}`)},
		{name: "malformed plugin PAT", raw: []byte(`{"type":"codex","auth_kind":"pat","access_token":"` + malformedCollisionPAT + `"}`)},
		{name: "different account PAT", raw: differentAccountRaw},
	} {
		t.Run("canonical collision "+collision.name, func(t *testing.T) {
			collisionPath := filepath.Join(env.authDir, canonicalCollisionName)
			if errWrite := os.WriteFile(collisionPath, collision.raw, 0o600); errWrite != nil {
				t.Fatalf("write collision fixture: %v", errWrite)
			}
			_ = waitForAuthIndex(t, client, env.baseURL, canonicalCollisionName)
			beforeCollision, errRead := os.ReadFile(collisionPath)
			if errRead != nil {
				t.Fatalf("read collision fixture: %v", errRead)
			}
			status, body := doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": validPAT})
			if status < 400 {
				t.Fatalf("collision import status = %d, want error; body=%s", status, body)
			}
			assertNoSecret(t, body, allSecrets...)
			afterCollision, errRead := os.ReadFile(collisionPath)
			if errRead != nil {
				t.Fatalf("read collision fixture after import: %v", errRead)
			}
			if !bytes.Equal(beforeCollision, afterCollision) {
				t.Fatal("collision import modified the occupied canonical file")
			}
			status, body = doRequest(t, client, http.MethodDelete, env.baseURL+"/v0/management/auth-files?name="+canonicalCollisionName, managementKey, nil)
			if status != http.StatusOK {
				t.Fatalf("collision cleanup status = %d, want 200; body=%s", status, body)
			}
			waitForFileRemoval(t, collisionPath)
			waitForAuthIndexRemoval(t, client, env.baseURL, canonicalCollisionName)
		})
	}

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": validPAT})
	if status != http.StatusAccepted {
		t.Fatalf("valid import status = %d, want 202; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)

	whoamiRequest := backend.lastWhoami(t)
	if whoamiRequest.method != http.MethodGet || whoamiRequest.uri != whoamiPath {
		t.Fatalf("whoami request = %s %s", whoamiRequest.method, whoamiRequest.uri)
	}
	if got := whoamiRequest.header.Get("Authorization"); got != "Bearer "+validPAT {
		t.Fatalf("whoami Authorization header mismatch")
	}
	if got := whoamiRequest.header.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("whoami Originator = %q", got)
	}
	if got := whoamiRequest.header.Get("User-Agent"); !strings.HasPrefix(got, "codex-pat/") {
		t.Fatalf("whoami User-Agent = %q", got)
	}

	patPath := waitForPATFile(t, env.authDir)
	patName := filepath.Base(patPath)
	if want := patlogic.FileName("workspace-integration", "user-integration", "integration@example.invalid", "pro"); patName != want {
		t.Fatalf("canonical PAT filename = %q, want %q", patName, want)
	}
	fileInfo, err := os.Stat(patPath)
	if err != nil {
		t.Fatalf("stat persisted PAT: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("persisted PAT mode = %04o, want 0600", got)
	}
	persisted := readJSONObject(t, patPath)
	for key, want := range map[string]any{
		"type":         "codex",
		"auth_kind":    "pat",
		"access_token": validPAT,
		"account_id":   "workspace-integration",
		"plan_type":    "pro",
	} {
		if got := persisted[key]; got != want {
			t.Fatalf("persisted %s = %#v, want %#v", key, got, want)
		}
	}
	if disabled, exists := persisted["disabled"]; exists && disabled != false {
		t.Fatalf("persisted disabled = %#v, want false or omitted", disabled)
	}
	if got := persisted["chatgpt_user_id"]; got != "user-integration" {
		t.Fatalf("persisted chatgpt_user_id = %#v", got)
	}
	if got := persisted["chatgpt_account_is_fedramp"]; got != true {
		t.Fatalf("persisted FedRAMP flag = %#v, want true", got)
	}
	headers, _ := persisted["headers"].(map[string]any)
	if got := headers["X-OpenAI-Fedramp"]; got != "true" {
		t.Fatalf("persisted FedRAMP header = %#v, want true", got)
	}
	if validatedAt, _ := persisted["validated_at"].(string); validatedAt == "" {
		t.Fatal("persisted credential has no validation timestamp")
	}
	for _, forbidden := range []string{"api_key", "refresh_token", "id_token", "expires_at", "expiry"} {
		if _, exists := persisted[forbidden]; exists {
			t.Fatalf("persisted PAT contains forbidden field %q", forbidden)
		}
	}
	beforeRejectedReplacement := readJSONObject(t, patPath)
	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": invalidPAT})
	if status < 400 {
		t.Fatalf("rejected replacement status = %d; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	afterRejectedReplacement := readJSONObject(t, patPath)
	for _, key := range []string{"access_token", "account_id", "chatgpt_user_id", "plan_type"} {
		if afterRejectedReplacement[key] != beforeRejectedReplacement[key] {
			t.Fatalf("failed replacement changed persisted %s", key)
		}
	}

	status, body = doRequest(t, client, http.MethodGet, env.baseURL+"/v0/management/plugins/codex-pat/status", managementKey, nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(patName)) || bytes.Contains(body, []byte("codex-oauth-fixture")) {
		t.Fatalf("redacted status response = %d %s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	if current, errRead := os.ReadFile(oauthPath); errRead != nil || !bytes.Equal(current, oauthFixture) {
		t.Fatalf("OAuth fixture was modified: err=%v body=%s", errRead, current)
	}

	authIndex := waitForAuthIndex(t, client, env.baseURL, patName)
	waitForPluginReadiness(t, client, env.baseURL, process.logPath, patName, "ready")
	beforeTransient, err := os.ReadFile(patPath)
	if err != nil {
		t.Fatalf("read PAT before transient revalidation: %v", err)
	}
	backend.setWhoami(validPAT, http.StatusServiceUnavailable, `{"error":"temporary fake failure"}`)
	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/revalidate", managementKey, map[string]string{"auth_index": authIndex})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("transient revalidation status = %d, want 503; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	afterTransient, err := os.ReadFile(patPath)
	if err != nil {
		t.Fatalf("read PAT after transient revalidation: %v", err)
	}
	if !bytes.Equal(beforeTransient, afterTransient) {
		t.Fatal("transient revalidation changed the saved credential")
	}

	backend.setWhoami(validPAT, http.StatusOK, validWhoami("user-integration", "workspace-integration", "integration@example.invalid", "team", true))
	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/revalidate", managementKey, map[string]string{"auth_index": authIndex})
	if status != http.StatusOK {
		t.Fatalf("successful revalidation status = %d, want 200; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	waitForCredentialField(t, patPath, "plan_type", "team")

	backend.setWhoami(validPAT, http.StatusUnauthorized, `{"error":"revoked fake PAT"}`)
	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/revalidate", managementKey, map[string]string{"auth_index": authIndex})
	if status < 400 || status >= 500 {
		t.Fatalf("definitive invalidation status = %d, want a domain client error; body=%s", status, body)
	}
	waitForCredentialField(t, patPath, "disabled", true)

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": replacementPAT})
	if status != http.StatusAccepted {
		t.Fatalf("replacement import status = %d, want 202; body=%s", status, body)
	}
	replacedCredential := waitForCredentialField(t, patPath, "access_token", replacementPAT)
	if got, exists := replacedCredential["disabled"]; exists && got != false {
		t.Fatalf("replacement import disabled = %#v, want false or omitted", got)
	}
	if got := replacedCredential["access_token"]; got != replacementPAT {
		t.Fatalf("same-workspace replacement did not update the saved fake PAT")
	}
	currentPAT = replacementPAT
	if files := waitForPATFileCount(t, env.authDir, 1); files[0] != patPath {
		t.Fatalf("same-principal replacement changed deterministic file: %v", files)
	}

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": sameWorkspaceOtherUserPAT})
	if status != http.StatusAccepted {
		t.Fatalf("same-workspace other-user import status = %d, want 202; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	principalFiles := waitForPATFileCount(t, env.authDir, 2)
	otherUserName := patlogic.FileName("workspace-integration", "user-other", "integration@example.invalid", "pro")
	otherUserPath := filepath.Join(env.authDir, otherUserName)
	if !slices.Contains(principalFiles, otherUserPath) || !slices.Contains(principalFiles, patPath) {
		t.Fatalf("same-workspace users did not retain distinct files: %v", principalFiles)
	}
	otherUserCredential := readJSONObject(t, otherUserPath)
	if got := otherUserCredential["access_token"]; got != sameWorkspaceOtherUserPAT {
		t.Fatalf("other user access token was not persisted independently")
	}
	if got := otherUserCredential["chatgpt_user_id"]; got != "user-other" {
		t.Fatalf("other user identity = %#v", got)
	}
	if got := otherUserCredential["account_id"]; got != "workspace-integration" {
		t.Fatalf("other user workspace = %#v", got)
	}
	otherUserAuthIndex := waitForAuthIndex(t, client, env.baseURL, otherUserName)
	if otherUserAuthIndex == authIndex {
		t.Fatalf("same-workspace users share auth_index %q", authIndex)
	}
	otherUserBeforeMismatch, err := os.ReadFile(otherUserPath)
	if err != nil {
		t.Fatalf("read other user before mismatch revalidation: %v", err)
	}

	backend.setWhoami(replacementPAT, http.StatusOK, validWhoami("user-other", "workspace-integration", "integration@example.invalid", "pro", true))
	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/revalidate", managementKey, map[string]string{"auth_index": authIndex})
	if status != http.StatusConflict || !bytes.Contains(body, []byte(`"code":"account_mismatch"`)) {
		t.Fatalf("cross-user revalidation status = %d, want 409 account_mismatch; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	mismatchedCredential := waitForCredentialField(t, patPath, "validation_state", "account_mismatch")
	if got := mismatchedCredential["disabled"]; got != true {
		t.Fatalf("cross-user mismatch disabled = %#v, want true", got)
	}
	if got := mismatchedCredential["chatgpt_user_id"]; got != "user-integration" {
		t.Fatalf("cross-user mismatch rebound saved user = %#v", got)
	}
	if got := mismatchedCredential["account_id"]; got != "workspace-integration" {
		t.Fatalf("cross-user mismatch rebound saved workspace = %#v", got)
	}
	otherUserAfterMismatch, err := os.ReadFile(otherUserPath)
	if err != nil {
		t.Fatalf("read other user after mismatch revalidation: %v", err)
	}
	if !bytes.Equal(otherUserBeforeMismatch, otherUserAfterMismatch) {
		t.Fatal("cross-user mismatch modified the other user's credential")
	}

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": principalReplacementPAT})
	if status != http.StatusAccepted {
		t.Fatalf("same-principal recovery import status = %d, want 202; body=%s", status, body)
	}
	assertNoSecret(t, body, allSecrets...)
	principalReplacement := waitForCredentialField(t, patPath, "access_token", principalReplacementPAT)
	if got := principalReplacement["chatgpt_user_id"]; got != "user-integration" {
		t.Fatalf("same-principal recovery user = %#v", got)
	}
	if got := principalReplacement["disabled"]; got != false {
		t.Fatalf("same-principal recovery disabled = %#v, want false", got)
	}
	otherUserAfterReplacement, err := os.ReadFile(otherUserPath)
	if err != nil {
		t.Fatalf("read other user after same-principal replacement: %v", err)
	}
	if !bytes.Equal(otherUserBeforeMismatch, otherUserAfterReplacement) {
		t.Fatal("same-principal replacement modified the other user's credential")
	}
	if files := waitForPATFileCount(t, env.authDir, 2); !slices.Contains(files, patPath) || !slices.Contains(files, otherUserPath) {
		t.Fatalf("same-principal replacement changed file set: %v", files)
	}
	currentPAT = principalReplacementPAT
	primaryBeforeSecondWorkspace, err := os.ReadFile(patPath)
	if err != nil {
		t.Fatalf("read primary user before second workspace import: %v", err)
	}
	otherUserBeforeSecondWorkspace, err := os.ReadFile(otherUserPath)
	if err != nil {
		t.Fatalf("read other user before second workspace import: %v", err)
	}

	status, body = doRequest(t, client, http.MethodPost, env.baseURL+"/v0/management/plugins/codex-pat/import", managementKey, map[string]string{"pat": secondWorkspacePAT})
	if status != http.StatusAccepted {
		t.Fatalf("second workspace import status = %d, want 202; body=%s", status, body)
	}
	workspaceFiles := waitForPATFileCount(t, env.authDir, 3)
	secondName := patlogic.FileName("workspace-second", "user-integration", "integration@example.invalid", "plus")
	secondPath := filepath.Join(env.authDir, secondName)
	if !slices.Contains(workspaceFiles, secondPath) {
		t.Fatalf("second workspace did not create a distinct auth file: %v", workspaceFiles)
	}
	secondCredential := readJSONObject(t, secondPath)
	if got := secondCredential["access_token"]; got != secondWorkspacePAT {
		t.Fatalf("second workspace access token was not persisted independently")
	}
	if got := secondCredential["account_id"]; got != "workspace-second" {
		t.Fatalf("second workspace account_id = %#v", got)
	}
	if got := secondCredential["chatgpt_user_id"]; got != "user-integration" {
		t.Fatalf("second workspace user ID = %#v", got)
	}
	if got := secondCredential["chatgpt_account_is_fedramp"]; got != false {
		t.Fatalf("second workspace FedRAMP flag = %#v, want false", got)
	}
	if _, exists := secondCredential["headers"]; exists {
		t.Fatalf("non-FedRAMP workspace persisted custom headers: %#v", secondCredential["headers"])
	}
	secondAuthIndex := waitForAuthIndex(t, client, env.baseURL, secondName)
	if secondAuthIndex == authIndex || secondAuthIndex == otherUserAuthIndex {
		t.Fatalf("distinct principal reused auth_index %q", secondAuthIndex)
	}
	primaryAfterSecondWorkspace, err := os.ReadFile(patPath)
	if err != nil {
		t.Fatalf("read primary user after second workspace import: %v", err)
	}
	otherUserAfterSecondWorkspace, err := os.ReadFile(otherUserPath)
	if err != nil {
		t.Fatalf("read other user after second workspace import: %v", err)
	}
	if !bytes.Equal(primaryBeforeSecondWorkspace, primaryAfterSecondWorkspace) || !bytes.Equal(otherUserBeforeSecondWorkspace, otherUserAfterSecondWorkspace) {
		t.Fatal("second workspace import modified an existing principal credential")
	}
	status, body = doRequest(t, client, http.MethodDelete, env.baseURL+"/v0/management/auth-files?name="+filepath.Base(secondPath), managementKey, nil)
	if status != http.StatusOK {
		t.Fatalf("second workspace cleanup status = %d, want 200; body=%s", status, body)
	}
	waitForPATFileCount(t, env.authDir, 2)
	status, body = doRequest(t, client, http.MethodDelete, env.baseURL+"/v0/management/auth-files?name="+otherUserName, managementKey, nil)
	if status != http.StatusOK {
		t.Fatalf("same-workspace other-user cleanup status = %d, want 200; body=%s", status, body)
	}
	waitForPATFileCount(t, env.authDir, 1)

	process.stop(t)

	persisted = readJSONObject(t, patPath)
	persisted["base_url"] = backend.server.URL
	updated, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("encode local base_url fixture: %v", err)
	}
	if errWrite := os.WriteFile(patPath, updated, 0o600); errWrite != nil {
		t.Fatalf("write local base_url fixture: %v", errWrite)
	}
	whoamiBeforeRestart, _, _ := backend.counts()

	writeCPAConfig(t, env, true)
	process = startCPA(t, env.cpaBinary, env.tempDir, env.configPath, authAPIBase, env.port)
	waitForPlugin(t, client, env.baseURL, process.logPath, env.pluginVersion, true)
	authIndex = waitForAuthIndex(t, client, env.baseURL, patName)
	whoamiAfterRestart, _, _ := backend.counts()
	if whoamiAfterRestart != whoamiBeforeRestart {
		t.Fatalf("restart parser performed network hydration: before=%d after=%d", whoamiBeforeRestart, whoamiAfterRestart)
	}

	model := waitForModel(t, client, env.baseURL, patName)
	requestBody, _ := json.Marshal(map[string]any{"model": model, "input": "integration smoke", "stream": false})
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/v1/responses", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("create native Codex request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+clientAPIKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("send native Codex request: %v", err)
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	assertNoSecret(t, responseBody, allSecrets...)

	codexRequest := backend.waitForCodexRequest(t)
	if got := codexRequest.header.Get("Authorization"); got != "Bearer "+currentPAT {
		t.Fatalf("native executor Authorization header mismatch")
	}
	if got := codexRequest.header.Get("Chatgpt-Account-Id"); got != "workspace-integration" {
		t.Fatalf("native executor account header = %q", got)
	}
	if got := codexRequest.header.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("native executor FedRAMP header = %q", got)
	}
	if got := codexRequest.header.Get("Originator"); got == "" {
		t.Fatal("native executor omitted Originator")
	}
	if strings.Contains(codexRequest.uri, currentPAT) {
		t.Fatal("native executor placed PAT in request URL")
	}
	_, _, oauthCalls := backend.counts()
	if oauthCalls != 0 {
		t.Fatalf("PAT request triggered %d OAuth token calls", oauthCalls)
	}

	process.stop(t)
	writeCPAConfig(t, env, false)
	process = startCPA(t, env.cpaBinary, env.tempDir, env.configPath, authAPIBase, env.port)
	waitForPlugin(t, client, env.baseURL, process.logPath, env.pluginVersion, false)
	status, _ = doRequest(t, client, http.MethodGet, env.baseURL+"/v0/resource/plugins/codex-pat/manage", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("disabled plugin resource status = %d, want 404", status)
	}
	status, _ = doRequest(t, client, http.MethodGet, env.baseURL+"/v0/management/plugins/codex-pat/status", managementKey, nil)
	if status != http.StatusNotFound {
		t.Fatalf("disabled plugin management status = %d, want 404", status)
	}
	degradedAuthIndex := waitForAuthIndex(t, client, env.baseURL, patName)
	if degradedAuthIndex == "" {
		t.Fatal("plugin-disabled generic parser did not retain the PAT auth")
	}
	_ = waitForModel(t, client, env.baseURL, patName)
	if _, errStat := os.Stat(patPath); errStat != nil {
		t.Fatalf("disabling plugin removed persisted auth: %v", errStat)
	}

	process.stop(t)
	writeCPAConfig(t, env, true)
	process = startCPA(t, env.cpaBinary, env.tempDir, env.configPath, authAPIBase, env.port)
	waitForPlugin(t, client, env.baseURL, process.logPath, env.pluginVersion, true)
	status, body = doRequest(t, client, http.MethodDelete, env.baseURL+"/v0/management/auth-files?name="+patName, managementKey, nil)
	if status != http.StatusOK {
		t.Fatalf("native auth-file deletion status = %d, want 200; body=%s", status, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, errStat := os.Stat(patPath)
		if errors.Is(errStat, os.ErrNotExist) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, errStat := os.Stat(patPath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("PAT auth file still exists after native deletion: %v", errStat)
	}

	process.stop(t)
	logs, err := os.ReadFile(process.logPath)
	if err != nil {
		t.Fatalf("read CPA integration log: %v", err)
	}
	assertNoSecret(t, logs, allSecrets...)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, request := range append(append([]observedRequest{}, backend.whoamiRequests...), backend.codexRequests...) {
		assertNoSecret(t, []byte(request.uri), allSecrets...)
	}
}
