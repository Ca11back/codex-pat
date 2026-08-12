package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	managementKey = "codex-pat-load-smoke-management-key"
	author        = "Ca11back"
	repositoryURL = "https://github.com/Ca11back/codex-pat"
)

type options struct {
	cpaBinary string
	plugin    string
	version   string
}

type pluginList struct {
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

type statusEnvelope struct {
	Data struct {
		Accounts []json.RawMessage `json:"accounts"`
	} `json:"data"`
}

func main() {
	var settings options
	flag.StringVar(&settings.cpaBinary, "cpa", "", "path to a native CPA v7.2.103 or v7.2.129 executable")
	flag.StringVar(&settings.plugin, "plugin", "", "path to the native codex-pat library")
	flag.StringVar(&settings.version, "version", "", "expected dotted plugin version")
	flag.Parse()
	if err := run(settings); err != nil {
		fmt.Fprintln(os.Stderr, "CPA load/register smoke failed:", err)
		os.Exit(1)
	}
}

func run(settings options) error {
	if strings.TrimSpace(settings.cpaBinary) == "" || strings.TrimSpace(settings.plugin) == "" {
		return errors.New("-cpa and -plugin are required")
	}
	if !validVersion(settings.version) {
		return fmt.Errorf("-version must be dotted numeric x.y.z, got %q", settings.version)
	}
	cpaBinary, err := filepath.Abs(settings.cpaBinary)
	if err != nil {
		return fmt.Errorf("resolve CPA path: %w", err)
	}
	pluginSource, err := filepath.Abs(settings.plugin)
	if err != nil {
		return fmt.Errorf("resolve plugin path: %w", err)
	}
	if err = requireFile(cpaBinary); err != nil {
		return fmt.Errorf("CPA binary: %w", err)
	}
	if err = requireFile(pluginSource); err != nil {
		return fmt.Errorf("plugin library: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "codex-pat-cpa-load-smoke-")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	authDir := filepath.Join(tempDir, "auth")
	pluginDir := filepath.Join(tempDir, "plugins")
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	if err = os.MkdirAll(pluginDir, 0o755); err != nil {
		return fmt.Errorf("create plugin directory: %w", err)
	}
	extension, err := libraryExtension(runtime.GOOS)
	if err != nil {
		return err
	}
	pluginPath := filepath.Join(pluginDir, "codex-pat-v"+settings.version+extension)
	if err = copyFile(pluginSource, pluginPath, 0o755); err != nil {
		return err
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	configPath := filepath.Join(tempDir, "config.yaml")
	config := fmt.Sprintf(`host: "127.0.0.1"
port: %d
remote-management:
  allow-remote: true
  secret-key: %s
  disable-control-panel: true
  disable-auto-update-panel: true
auth-dir: %s
debug: false
logging-to-file: false
plugins:
  enabled: true
  dir: %s
  configs:
    codex-pat:
      enabled: true
      store:
        version: %s
`, port, strconv.Quote(managementKey), strconv.Quote(authDir), strconv.Quote(pluginDir), strconv.Quote(settings.version))
	if err = os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write CPA config: %w", err)
	}

	logPath := filepath.Join(tempDir, "cpa.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create CPA log: %w", err)
	}
	command := exec.Command(cpaBinary, "-config", configPath, "-local-model")
	command.Dir = tempDir
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start CPA: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		if command.Process != nil && command.ProcessState == nil {
			_ = command.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		}
		_ = logFile.Close()
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err = waitForRegistration(baseURL, settings.version, done); err != nil {
		_ = logFile.Sync()
		logs, _ := os.ReadFile(logPath)
		return fmt.Errorf("%w\nCPA logs:\n%s", err, logs)
	}
	if err = requireEmptyDirectory(authDir); err != nil {
		return fmt.Errorf("auth directory before host callback: %w", err)
	}
	if err = waitForEmptyStatusHostCallback(baseURL, done); err != nil {
		_ = logFile.Sync()
		logs, _ := os.ReadFile(logPath)
		return fmt.Errorf("%w\nCPA logs:\n%s", err, logs)
	}
	if err = requireEmptyDirectory(authDir); err != nil {
		return fmt.Errorf("auth directory after host callback: %w", err)
	}
	fmt.Printf("CPA registered codex-pat %s and completed status -> host.auth.list on %s/%s\n", settings.version, runtime.GOOS, runtime.GOARCH)
	return nil
}

func validVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func libraryExtension(goos string) (string, error) {
	switch goos {
	case "linux":
		return ".so", nil
	case "darwin":
		return ".dylib", nil
	case "windows":
		return ".dll", nil
	default:
		return "", fmt.Errorf("unsupported smoke platform: %s/%s", goos, runtime.GOARCH)
	}
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("must be a non-empty regular file")
	}
	return nil
}

func requireEmptyDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return fmt.Errorf("must remain empty, found %v", names)
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open plugin library: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create smoke plugin library: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy plugin library: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close plugin library: %w", closeErr)
	}
	return nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate CPA port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitForRegistration(baseURL, version string, done <-chan error) error {
	client := &http.Client{Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var lastResponse string
	for {
		select {
		case processErr := <-done:
			return fmt.Errorf("CPA exited before registration: %v", processErr)
		case <-ctx.Done():
			return fmt.Errorf("registration timed out; last response: %s", lastResponse)
		case <-time.After(100 * time.Millisecond):
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v0/management/plugins", nil)
		if err != nil {
			return fmt.Errorf("create management request: %w", err)
		}
		request.Header.Set("X-Management-Key", managementKey)
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		lastResponse = string(body)
		if readErr != nil || response.StatusCode != http.StatusOK {
			continue
		}
		var plugins pluginList
		if json.Unmarshal(body, &plugins) != nil {
			continue
		}
		for _, plugin := range plugins.Plugins {
			if plugin.ID != "codex-pat" || !plugin.Registered || !plugin.EffectiveEnabled || plugin.Metadata == nil {
				continue
			}
			if plugin.Metadata.Name != "Codex PAT" || plugin.Metadata.Version != version ||
				plugin.Metadata.Author != author || plugin.Metadata.GitHubRepository != repositoryURL {
				continue
			}
			if len(plugin.Menus) != 1 || plugin.Menus[0].Menu != "Codex PAT" ||
				plugin.Menus[0].Path != "/v0/resource/plugins/codex-pat/manage" {
				continue
			}
			return nil
		}
	}
}

func waitForEmptyStatusHostCallback(baseURL string, done <-chan error) error {
	client := &http.Client{Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var lastResponse string
	for {
		select {
		case processErr := <-done:
			return fmt.Errorf("CPA exited before host callback: %v", processErr)
		case <-ctx.Done():
			return fmt.Errorf("status host callback timed out; last response: %s", lastResponse)
		case <-time.After(100 * time.Millisecond):
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v0/management/plugins/codex-pat/status", nil)
		if err != nil {
			return fmt.Errorf("create status request: %w", err)
		}
		request.Header.Set("X-Management-Key", managementKey)
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		lastResponse = string(body)
		if readErr != nil || response.StatusCode != http.StatusOK {
			continue
		}
		if err = validateEmptyStatusResponse(body); err != nil {
			return err
		}
		return nil
	}
}

func validateEmptyStatusResponse(body []byte) error {
	var status statusEnvelope
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("decode status host-callback response: %w", err)
	}
	if status.Data.Accounts == nil {
		return fmt.Errorf("status host-callback response omitted accounts: %s", body)
	}
	if len(status.Data.Accounts) != 0 {
		return fmt.Errorf("status host-callback response found credentials without a PAT: %s", body)
	}
	return nil
}
