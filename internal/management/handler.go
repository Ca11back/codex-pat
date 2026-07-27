package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"oaipat/internal/hostrpc"
	"oaipat/internal/pat"
	"oaipat/internal/plugin"
)

const maxManagementBodyBytes = pat.MaxTokenBytes + 1024

const watcherReadySettleDelay = 250 * time.Millisecond

type lifecycle interface {
	Import(context.Context, string, string) (pat.SaveResult, error)
	Revalidate(context.Context, string, pat.Credential, string) (pat.RevalidationResult, error)
}

type Handler struct {
	host       hostrpc.API
	service    lifecycle
	mutationMu sync.Mutex
}

func New(host hostrpc.API, service *pat.Service) *Handler {
	return &Handler{host: host, service: service}
}

func (h *Handler) Register(_ context.Context, _ pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodPost, Path: managementImportPath},
			{Method: http.MethodGet, Path: managementStatusPath},
			{Method: http.MethodPost, Path: managementRevalidatePath},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        resourceManagePath,
				Menu:        "Codex PAT",
				Description: "Manage Codex PAT credentials.",
			},
			{Path: resourceCSSPath},
			{Path: resourceJSPath},
			{Path: resourceRefreshIconPath},
			{Path: resourceTrashIconPath},
			{Path: resourceKeyIconPath},
		},
	}, nil
}

func (h *Handler) Handle(ctx context.Context, request plugin.ManagementRequest) (pluginapi.ManagementResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	if path == "" {
		path = "/"
	}

	if method == http.MethodGet {
		if response, ok := resourceResponse(path); ok {
			return response, nil
		}
	}

	switch {
	case method == http.MethodPost && path == managementImportPath:
		return h.handleImport(ctx, request.HostCallbackID, request.Body), nil
	case method == http.MethodGet && path == managementStatusPath:
		return h.handleStatus(ctx, request.HostCallbackID), nil
	case method == http.MethodPost && path == managementRevalidatePath:
		return h.handleRevalidate(ctx, request.HostCallbackID, request.Body), nil
	default:
		return jsonErrorResponse(http.StatusNotFound, "route_not_found", "The requested plugin route was not found.", false), nil
	}
}

func (h *Handler) handleImport(ctx context.Context, callbackID string, body []byte) pluginapi.ManagementResponse {
	if h == nil || h.service == nil {
		return jsonErrorResponse(http.StatusServiceUnavailable, "service_unavailable", "PAT management is unavailable.", true)
	}
	var request importRequest
	if err := decodeStrictBody(body, &request); err != nil {
		return requestErrorResponse(err)
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	result, err := h.service.Import(ctx, callbackID, request.PAT)
	request.PAT = ""
	if err != nil {
		return patFailureResponse(err)
	}
	status := statusFromCredential(result.Credential, "", result.FileName, false, false, "pending")
	return jsonDataResponse(http.StatusAccepted, status)
}

func (h *Handler) handleStatus(ctx context.Context, callbackID string) pluginapi.ManagementResponse {
	statuses, err := h.listStatuses(ctx, callbackID)
	if err != nil {
		return jsonErrorResponse(http.StatusServiceUnavailable, "host_unavailable", "Credential status is temporarily unavailable.", true)
	}
	return jsonDataResponse(http.StatusOK, accountList{Accounts: statuses})
}

func (h *Handler) handleRevalidate(ctx context.Context, callbackID string, body []byte) pluginapi.ManagementResponse {
	if h == nil || h.host == nil || h.service == nil {
		return jsonErrorResponse(http.StatusServiceUnavailable, "service_unavailable", "PAT management is unavailable.", true)
	}
	var request revalidateRequest
	if err := decodeStrictBody(body, &request); err != nil {
		return requestErrorResponse(err)
	}
	request.AuthIndex = strings.TrimSpace(request.AuthIndex)
	if request.AuthIndex == "" {
		return jsonErrorResponse(http.StatusBadRequest, "invalid_request", "auth_index is required.", false)
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	physical, err := h.host.AuthGet(ctx, callbackID, request.AuthIndex)
	if err != nil {
		return jsonErrorResponse(http.StatusNotFound, "credential_not_found", "The PAT credential was not found.", false)
	}
	credential, owned, err := pat.DecodeOwnedCredential(physical.JSON)
	if !owned || !pat.IsOwnedFile(physical.Name) {
		return jsonErrorResponse(http.StatusNotFound, "credential_not_found", "The PAT credential was not found.", false)
	}
	if err != nil || strings.TrimSpace(physical.Name) == "" {
		return jsonErrorResponse(http.StatusUnprocessableEntity, "credential_malformed", "The PAT credential is malformed and cannot be revalidated.", false)
	}
	if !pat.IsOwnedFileForPrincipal(physical.Name, credential.AccountID, credential.ChatGPTUserID) {
		return jsonErrorResponse(http.StatusUnprocessableEntity, "credential_malformed", "The PAT credential filename does not match its user and workspace.", false)
	}

	result, err := h.service.Revalidate(ctx, callbackID, credential, physical.Name)
	if err != nil {
		return patFailureResponse(err)
	}
	switch result.Outcome {
	case pat.RevalidationInvalid:
		return jsonErrorResponse(http.StatusUnprocessableEntity, "pat_invalid", "PAT validation failed and the credential was disabled.", false)
	case pat.RevalidationAccountMismatch:
		return jsonErrorResponse(http.StatusConflict, "account_mismatch", "The PAT belongs to a different user or workspace and the credential was disabled.", false)
	case pat.RevalidationValid:
		status := statusFromCredential(result.Credential, request.AuthIndex, result.FileName, false, false, "pending")
		return jsonDataResponse(http.StatusOK, status)
	default:
		return jsonErrorResponse(http.StatusInternalServerError, "unexpected_outcome", "PAT revalidation returned an unexpected result.", false)
	}
}

func (h *Handler) listStatuses(ctx context.Context, callbackID string) ([]accountStatus, error) {
	if h == nil || h.host == nil {
		return nil, errors.New("host unavailable")
	}
	files, err := h.host.AuthList(ctx, callbackID)
	if err != nil {
		return nil, err
	}
	statuses := make([]accountStatus, 0)
	for _, file := range files {
		status, ok := h.statusForFile(ctx, callbackID, file)
		if ok {
			statuses = append(statuses, status)
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		return strings.ToLower(statuses[i].Name) < strings.ToLower(statuses[j].Name)
	})
	return statuses, nil
}

func (h *Handler) statusForFile(ctx context.Context, callbackID string, file pluginapi.HostAuthFileEntry) (accountStatus, bool) {
	provider := strings.TrimSpace(file.Provider)
	if provider == "" {
		provider = strings.TrimSpace(file.Type)
	}
	if provider != "" && !strings.EqualFold(provider, pat.Provider) {
		return accountStatus{}, false
	}
	authIndex := strings.TrimSpace(file.AuthIndex)
	if authIndex == "" {
		return accountStatus{}, false
	}
	physical, err := h.host.AuthGet(ctx, callbackID, authIndex)
	if err != nil {
		return accountStatus{}, false
	}
	physicalName := strings.TrimSpace(physical.Name)
	if physicalName == "" {
		physicalName = strings.TrimSpace(file.Name)
	}
	if !pat.IsOwnedFile(physicalName) {
		return accountStatus{}, false
	}
	credential, owned, decodeErr := pat.DecodeOwnedCredential(physical.JSON)
	if !owned {
		if decodeErr == nil {
			return accountStatus{}, false
		}
		return accountStatus{
			AuthIndex:   authIndex,
			Name:        physicalName,
			Disabled:    true,
			Unavailable: true,
			Readiness:   "disabled",
		}, true
	}
	if decodeErr != nil || credential.Validate() != nil {
		return accountStatus{
			AuthIndex:   authIndex,
			Name:        physicalName,
			Disabled:    true,
			Unavailable: true,
			Readiness:   "disabled",
		}, true
	}
	if !pat.IsOwnedFileForPrincipal(physicalName, credential.AccountID, credential.ChatGPTUserID) {
		return accountStatus{}, false
	}

	disabled := credential.Disabled || file.Disabled
	unavailable := file.Unavailable
	readiness := "pending"
	if disabled {
		readiness = "disabled"
	} else if unavailable {
		readiness = "unavailable"
	} else if runtime, runtimeErr := h.host.AuthGetRuntime(ctx, callbackID, authIndex); runtimeErr == nil {
		disabled = disabled || runtime.Auth.Disabled
		unavailable = unavailable || runtime.Auth.Unavailable
		switch {
		case disabled:
			readiness = "disabled"
		case unavailable:
			readiness = "unavailable"
		case watcherUpdateSettled(file, runtime.Auth, time.Now()):
			readiness = "ready"
		}
	}
	return statusFromCredential(credential, authIndex, physicalName, disabled, unavailable, readiness), true
}

func watcherUpdateSettled(file, runtime pluginapi.HostAuthFileEntry, now time.Time) bool {
	if file.ModTime.IsZero() || runtime.UpdatedAt.IsZero() || now.IsZero() {
		return false
	}
	if runtime.UpdatedAt.Before(file.ModTime) || now.Before(runtime.UpdatedAt) {
		return false
	}
	return now.Sub(runtime.UpdatedAt) >= watcherReadySettleDelay
}

func statusFromCredential(credential pat.Credential, authIndex, name string, disabled, unavailable bool, readiness string) accountStatus {
	validatedAt := ""
	if !credential.ValidatedAt.IsZero() {
		validatedAt = credential.ValidatedAt.UTC().Format(time.RFC3339)
	}
	return accountStatus{
		AuthIndex:   strings.TrimSpace(authIndex),
		Name:        strings.TrimSpace(name),
		Email:       strings.TrimSpace(credential.Email),
		AccountID:   strings.TrimSpace(credential.AccountID),
		PlanType:    strings.TrimSpace(credential.PlanType),
		ValidatedAt: validatedAt,
		Disabled:    disabled || credential.Disabled,
		Unavailable: unavailable,
		Readiness:   readiness,
	}
}

func decodeStrictBody(body []byte, target any) error {
	if len(body) == 0 {
		return errors.New("request body is required")
	}
	if len(body) > maxManagementBodyBytes {
		return errBodyTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request JSON is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request JSON is invalid")
	}
	return nil
}

var errBodyTooLarge = errors.New("request body is too large")

func requestErrorResponse(err error) pluginapi.ManagementResponse {
	if errors.Is(err, errBodyTooLarge) {
		return jsonErrorResponse(http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.", false)
	}
	return jsonErrorResponse(http.StatusBadRequest, "invalid_request", "The request body is invalid.", false)
}

func patFailureResponse(err error) pluginapi.ManagementResponse {
	kind, retryable, upstreamStatus := pat.ErrorDetails(err)
	switch kind {
	case pat.ErrorInvalidInput:
		status := http.StatusBadRequest
		if upstreamStatus == http.StatusRequestEntityTooLarge {
			status = upstreamStatus
		}
		return jsonErrorResponse(status, string(kind), "A valid Codex PAT beginning with at- is required.", false)
	case pat.ErrorAuthentication, pat.ErrorRejected:
		return jsonErrorResponse(http.StatusUnprocessableEntity, string(kind), "The Codex Auth API rejected the PAT.", false)
	case pat.ErrorRateLimited:
		return jsonErrorResponse(http.StatusTooManyRequests, string(kind), "PAT validation was rate limited. Retry later.", true)
	case pat.ErrorTransient:
		return jsonErrorResponse(http.StatusServiceUnavailable, string(kind), "PAT validation is temporarily unavailable.", true)
	case pat.ErrorMalformedResponse:
		return jsonErrorResponse(http.StatusBadGateway, string(kind), "The Codex Auth API returned invalid account metadata.", false)
	case pat.ErrorAccountMismatch:
		return jsonErrorResponse(http.StatusConflict, string(kind), "The PAT belongs to a different user or workspace.", false)
	case pat.ErrorPersistence:
		return jsonErrorResponse(http.StatusInternalServerError, string(kind), "The PAT credential could not be saved securely.", false)
	default:
		return jsonErrorResponse(http.StatusInternalServerError, "internal_error", "The PAT operation failed.", retryable)
	}
}
