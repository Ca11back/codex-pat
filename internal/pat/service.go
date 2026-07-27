package pat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type Host interface {
	HTTPDoLimited(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error)
	AuthList(context.Context, string) ([]pluginapi.HostAuthFileEntry, error)
	AuthGet(context.Context, string, string) (pluginapi.HostAuthGetResponse, error)
	AuthSave(context.Context, string, string, json.RawMessage) (pluginapi.HostAuthSaveResponse, error)
}

const ValidationTimeout = 30 * time.Second

const (
	watcherSyncTimeout      = 5 * time.Second
	watcherSyncPollInterval = 25 * time.Millisecond
	watcherSyncStableWindow = 250 * time.Millisecond
)

type Service struct {
	host    Host
	version string
	baseURL string
	now     func() time.Time
	sync    watcherSyncConfig
}

type watcherSyncConfig struct {
	timeout      time.Duration
	pollInterval time.Duration
	stableWindow time.Duration
}

type Option func(*Service)

func WithAuthAPIBaseURL(baseURL string) Option {
	return func(service *Service) {
		service.baseURL = strings.TrimSpace(baseURL)
	}
}

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

func withWatcherSyncTiming(timeout, pollInterval, stableWindow time.Duration) Option {
	return func(service *Service) {
		service.sync = watcherSyncConfig{
			timeout:      timeout,
			pollInterval: pollInterval,
			stableWindow: stableWindow,
		}
	}
}

func NewService(host Host, version string, options ...Option) *Service {
	service := &Service{
		host:    host,
		version: safeVersion(version),
		baseURL: strings.TrimSpace(os.Getenv("CODEX_AUTHAPI_BASE_URL")),
		now:     time.Now,
		sync: watcherSyncConfig{
			timeout:      watcherSyncTimeout,
			pollInterval: watcherSyncPollInterval,
			stableWindow: watcherSyncStableWindow,
		},
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

type SaveResult struct {
	Credential Credential
	FileName   string
	Path       string
}

func (s *Service) Validate(ctx context.Context, callbackID, token string) (Identity, error) {
	if s == nil {
		return Identity{}, newError(ErrorTransient, "PAT service is unavailable", 0, true, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	validationCtx, cancel := context.WithTimeout(ctx, ValidationTimeout)
	defer cancel()
	return FetchIdentity(validationCtx, s.host, callbackID, s.baseURL, s.version, token)
}

func (s *Service) Import(ctx context.Context, callbackID, rawToken string) (SaveResult, error) {
	token, err := NormalizeToken(rawToken)
	if err != nil {
		return SaveResult{}, err
	}
	identity, err := s.Validate(ctx, callbackID, token)
	if err != nil {
		return SaveResult{}, err
	}
	credential := NewCredential(token, identity, s.now().UTC())
	return s.SaveCredential(ctx, callbackID, credential)
}

func (s *Service) SaveCredential(ctx context.Context, callbackID string, credential Credential) (SaveResult, error) {
	return s.saveCredential(ctx, callbackID, credential, "")
}

func (s *Service) saveCredential(ctx context.Context, callbackID string, credential Credential, preferredFileName string) (SaveResult, error) {
	if s == nil || s.host == nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential store is unavailable", 0, false, nil)
	}
	if err := credential.Validate(); err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential is incomplete", 0, false, err)
	}
	fileName, err := s.resolveFileName(ctx, callbackID, credential, preferredFileName)
	if err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential filename could not be resolved", 0, false, err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential could not be encoded", 0, false, err)
	}
	hostPayload, err := failClosedHostPayload(raw)
	if err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential could not be encoded", 0, false, err)
	}
	saved, err := s.host.AuthSave(ctx, callbackID, fileName, hostPayload)
	if err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential could not be saved", 0, false, err)
	}
	if strings.TrimSpace(saved.Name) != fileName {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential store returned an unexpected file", 0, false, nil)
	}
	if err := s.awaitWatcherStage(ctx, callbackID, fileName, saved.Path); err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential staging did not reach stable host storage", 0, false, err)
	}
	if err := rewriteSecuredCredential(saved.Path, fileName, raw); err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential permissions could not be secured", 0, false, err)
	}
	if err := s.awaitWatcherPersistence(ctx, callbackID, fileName, saved.Path, raw, credential.Disabled); err != nil {
		return SaveResult{}, newError(ErrorPersistence, "PAT credential did not reach stable host storage", 0, false, err)
	}
	return SaveResult{Credential: credential, FileName: fileName, Path: saved.Path}, nil
}

type runtimeHost interface {
	AuthGetRuntime(context.Context, string, string) (pluginapi.HostAuthGetRuntimeResponse, error)
}

func (s *Service) awaitWatcherStage(ctx context.Context, callbackID, fileName, path string) error {
	host, ok := s.host.(runtimeHost)
	if !ok {
		return nil
	}
	return s.awaitWatcherState(ctx, host, callbackID, fileName, path, func(info os.FileInfo, runtime pluginapi.HostAuthFileEntry) bool {
		return runtime.Disabled && !runtime.UpdatedAt.Before(info.ModTime())
	}, nil)
}

func (s *Service) awaitWatcherPersistence(ctx context.Context, callbackID, fileName, path string, desired []byte, disabled bool) error {
	host, ok := s.host.(runtimeHost)
	if !ok {
		return nil
	}
	return s.awaitWatcherState(ctx, host, callbackID, fileName, path, func(info os.FileInfo, runtime pluginapi.HostAuthFileEntry) bool {
		return runtime.Disabled == disabled && !runtime.UpdatedAt.Before(info.ModTime())
	}, desired)
}

func (s *Service) awaitWatcherState(
	ctx context.Context,
	host runtimeHost,
	callbackID, fileName, path string,
	runtimeReady func(os.FileInfo, pluginapi.HostAuthFileEntry) bool,
	desired []byte,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.sync.timeout
	if timeout <= 0 {
		timeout = watcherSyncTimeout
	}
	pollInterval := s.sync.pollInterval
	if pollInterval <= 0 {
		pollInterval = watcherSyncPollInterval
	}
	stableWindow := s.sync.stableWindow
	if stableWindow < 0 {
		stableWindow = 0
	}
	deadline := time.Now().Add(timeout)
	var stableSince time.Time
	var stableModTime time.Time

	for {
		current, info, err := readSecuredCredential(path, fileName)
		if err != nil {
			return err
		}
		if len(desired) > 0 && !credentialPayloadEqual(current, desired) {
			if err := rewriteSecuredCredential(path, fileName, desired); err != nil {
				return err
			}
			stableSince = time.Time{}
			stableModTime = time.Time{}
		} else {
			authIndex, indexErr := s.authIndexForFile(ctx, callbackID, fileName)
			if indexErr == nil {
				runtime, runtimeErr := host.AuthGetRuntime(ctx, callbackID, authIndex)
				now := time.Now()
				if runtimeErr == nil && runtimeReady(info, runtime.Auth) {
					if stableSince.IsZero() || !stableModTime.Equal(info.ModTime()) {
						stableSince = now
						stableModTime = info.ModTime()
					}
					if now.Sub(stableSince) >= stableWindow {
						confirmed, confirmedInfo, confirmErr := readSecuredCredential(path, fileName)
						if confirmErr != nil {
							return confirmErr
						}
						contentReady := len(desired) == 0 || credentialPayloadEqual(confirmed, desired)
						confirmedRuntime, confirmedRuntimeErr := host.AuthGetRuntime(ctx, callbackID, authIndex)
						if confirmedRuntimeErr == nil && stableModTime.Equal(confirmedInfo.ModTime()) && contentReady && runtimeReady(confirmedInfo, confirmedRuntime.Auth) {
							return nil
						}
						stableSince = time.Time{}
						stableModTime = time.Time{}
					}
				} else {
					stableSince = time.Time{}
					stableModTime = time.Time{}
				}
			}
		}

		if time.Now().After(deadline) {
			return errors.New("timed out waiting for CPA auth watcher")
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) authIndexForFile(ctx context.Context, callbackID, fileName string) (string, error) {
	files, err := s.host.AuthList(ctx, callbackID)
	if err != nil {
		return "", err
	}
	authIndex := ""
	for _, file := range files {
		if file.RuntimeOnly || strings.TrimSpace(file.Name) != fileName {
			continue
		}
		candidate := strings.TrimSpace(file.AuthIndex)
		if candidate == "" {
			continue
		}
		if authIndex != "" && authIndex != candidate {
			return "", fmt.Errorf("multiple runtime auth records use the saved filename")
		}
		authIndex = candidate
	}
	if authIndex == "" {
		return "", errors.New("saved auth file is not registered")
	}
	return authIndex, nil
}

func readSecuredCredential(path, expectedName string) ([]byte, os.FileInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.Base(path) != expectedName {
		return nil, nil, errors.New("host returned an unexpected auth file path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect saved auth file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("saved auth file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("saved auth file must be regular")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read saved auth file: %w", err)
	}
	return raw, info, nil
}

func credentialPayloadEqual(left, right []byte) bool {
	leftValue, err := decodeComparableJSON(left)
	if err != nil {
		return false
	}
	rightValue, err := decodeComparableJSON(right)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func decodeComparableJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("credential JSON contains trailing data")
		}
		return nil, err
	}
	return value, nil
}

func (s *Service) resolveFileName(ctx context.Context, callbackID string, credential Credential, preferredFileName string) (string, error) {
	canonicalName := FileName(credential.AccountID, credential.ChatGPTUserID, credential.Email, credential.PlanType)
	currentPrincipalHash := principalHash(credential.AccountID, credential.ChatGPTUserID)
	preferredFileName = strings.TrimSpace(preferredFileName)
	if preferredFileName != "" && !isSafeAuthFileName(preferredFileName) {
		return "", errors.New("preferred auth filename is invalid")
	}

	files, err := s.host.AuthList(ctx, callbackID)
	if err != nil {
		return "", errors.New("list existing auth files")
	}

	canonicalOccupied := false
	samePrincipalFiles := make(map[string]string)
	for _, file := range files {
		if file.RuntimeOnly {
			continue
		}
		name := strings.TrimSpace(file.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, canonicalName) {
			canonicalOccupied = true
		}

		provider := strings.TrimSpace(file.Provider)
		if provider == "" {
			provider = strings.TrimSpace(file.Type)
		}
		candidate := strings.EqualFold(provider, Provider) || IsOwnedFile(name) || strings.EqualFold(name, canonicalName)
		if !candidate {
			continue
		}
		authIndex := strings.TrimSpace(file.AuthIndex)
		if authIndex == "" {
			return "", errors.New("candidate auth file has no stable index")
		}
		physical, getErr := s.host.AuthGet(ctx, callbackID, authIndex)
		if getErr != nil {
			return "", errors.New("inspect candidate auth file")
		}
		physicalName := strings.TrimSpace(physical.Name)
		if physicalName == "" {
			physicalName = name
		}
		if !isSafeAuthFileName(physicalName) {
			return "", errors.New("candidate auth filename is invalid")
		}
		if strings.EqualFold(physicalName, canonicalName) {
			canonicalOccupied = true
		}

		existing, owned, decodeErr := DecodeOwnedCredential(physical.JSON)
		if owned && decodeErr == nil && IsOwnedFile(physicalName) && !IsOwnedFileForPrincipal(physicalName, existing.AccountID, existing.ChatGPTUserID) {
			return "", errors.New("plugin-owned auth filename does not match its principal")
		}
		if owned && decodeErr == nil && samePrincipal(existing.AccountID, existing.ChatGPTUserID, credential.AccountID, credential.ChatGPTUserID) {
			if !IsOwnedFile(physicalName) {
				continue
			}
			key := strings.ToLower(physicalName)
			if current, ok := samePrincipalFiles[key]; ok && current != physicalName {
				return "", errors.New("same-principal auth filenames differ only by case")
			}
			samePrincipalFiles[key] = physicalName
			continue
		}
		if owned && IsOwnedFile(physicalName) && (decodeErr != nil || strings.TrimSpace(existing.AccountID) == "" || strings.TrimSpace(existing.ChatGPTUserID) == "") {
			return "", errors.New("plugin-owned auth file is malformed")
		}
		if descriptor, strictName := parseOwnedFile(physicalName); strictName && (owned || decodeErr != nil) {
			if descriptor.hash == currentPrincipalHash {
				return "", errors.New("same-principal plugin filename cannot be safely reused")
			}
		}
	}

	if preferredFileName != "" {
		for _, name := range samePrincipalFiles {
			if strings.EqualFold(name, preferredFileName) {
				return name, nil
			}
		}
		return "", errors.New("preferred auth file is not owned by the principal")
	}
	if len(samePrincipalFiles) > 1 {
		return "", errors.New("multiple PAT files exist for the principal")
	}
	for _, name := range samePrincipalFiles {
		return name, nil
	}
	if canonicalOccupied {
		return "", errors.New("canonical auth filename is already occupied")
	}
	return canonicalName, nil
}

func isSafeAuthFileName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\") && strings.HasSuffix(strings.ToLower(name), ".json")
}

// failClosedHostPayload keeps host.auth.save's immediate generic runtime from
// exposing a bearer before the watcher has parsed the final plugin-owned file.
// It also makes the interim and final file hashes differ, so CPA cannot skip the
// final watcher update as an unchanged rewrite. The empty bearer makes the
// plugin parser materialize a disabled staging auth, which acts as an observable
// barrier before the authoritative credential is written.
func failClosedHostPayload(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("disabled credential payload is invalid")
	}
	fields["access_token"] = json.RawMessage(`""`)
	fields["disabled"] = json.RawMessage("true")
	return json.Marshal(fields)
}

type RevalidationOutcome string

const (
	RevalidationValid           RevalidationOutcome = "valid"
	RevalidationInvalid         RevalidationOutcome = "invalid"
	RevalidationAccountMismatch RevalidationOutcome = "account_mismatch"
)

type RevalidationResult struct {
	Outcome RevalidationOutcome
	SaveResult
}

func (s *Service) Revalidate(ctx context.Context, callbackID string, credential Credential, fileName string) (RevalidationResult, error) {
	if err := credential.Validate(); err != nil {
		return RevalidationResult{}, newError(ErrorInvalidInput, "saved PAT credential is malformed", 0, false, err)
	}
	identity, err := s.Validate(ctx, callbackID, credential.AccessToken)
	if err != nil {
		if !IsAuthenticationFailure(err) {
			return RevalidationResult{}, err
		}
		credential.Disable(ValidationInvalid, s.now().UTC())
		saved, saveErr := s.saveCredential(ctx, callbackID, credential, fileName)
		if saveErr != nil {
			return RevalidationResult{}, saveErr
		}
		return RevalidationResult{Outcome: RevalidationInvalid, SaveResult: saved}, nil
	}
	if !samePrincipal(identity.AccountID, identity.UserID, credential.AccountID, credential.ChatGPTUserID) {
		credential.Disable(ValidationMismatch, s.now().UTC())
		saved, saveErr := s.saveCredential(ctx, callbackID, credential, fileName)
		if saveErr != nil {
			return RevalidationResult{}, saveErr
		}
		return RevalidationResult{Outcome: RevalidationAccountMismatch, SaveResult: saved}, nil
	}
	credential.ApplyIdentity(identity, s.now().UTC())
	saved, err := s.saveCredential(ctx, callbackID, credential, fileName)
	if err != nil {
		return RevalidationResult{}, err
	}
	return RevalidationResult{Outcome: RevalidationValid, SaveResult: saved}, nil
}

func ErrorDetails(err error) (kind ErrorKind, retryable bool, status int) {
	var patErr *Error
	if errors.As(err, &patErr) {
		return patErr.Kind, patErr.Retryable, patErr.HTTPStatus
	}
	return ErrorTransient, false, 0
}
