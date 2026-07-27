package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"oaipat/internal/pat"
)

type ManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type Management interface {
	Register(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error)
	Handle(context.Context, ManagementRequest) (pluginapi.ManagementResponse, error)
}

type Dispatcher struct {
	version    string
	management Management
}

func NewDispatcher(version string, management Management) *Dispatcher {
	return &Dispatcher{version: strings.TrimSpace(version), management: management}
}

func (d *Dispatcher) Dispatch(method string, request []byte) (response []byte) {
	defer func() {
		if recover() != nil {
			response = errorEnvelope("plugin_panic", "plugin request failed", false, http.StatusInternalServerError)
		}
	}()

	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := validateLifecycleRequest(request); err != nil {
			return errorEnvelope("unsupported_schema", err.Error(), false, http.StatusBadRequest)
		}
		return successEnvelope(newRegistration(d.version))
	case pluginabi.MethodAuthIdentifier:
		return successEnvelope(identifierResponse{Identifier: pat.Provider})
	case pluginabi.MethodAuthParse:
		var authRequest pluginapi.AuthParseRequest
		if err := decodeRequest(request, &authRequest); err != nil {
			return invalidRequestEnvelope(err)
		}
		return successEnvelope(pat.ParseAuth(authRequest))
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return errorEnvelope("unsupported_operation", "Codex PAT login is available only through the plugin management API", false, http.StatusNotImplemented)
	case pluginabi.MethodAuthRefresh:
		var refreshRequest pluginapi.AuthRefreshRequest
		if err := decodeRequest(request, &refreshRequest); err != nil {
			return invalidRequestEnvelope(err)
		}
		return successEnvelope(pat.RefreshAuth(refreshRequest))
	case pluginabi.MethodManagementRegister:
		if d == nil || d.management == nil {
			return errorEnvelope("management_unavailable", "plugin management is unavailable", false, http.StatusServiceUnavailable)
		}
		var registrationRequest pluginapi.ManagementRegistrationRequest
		if err := decodeRequest(request, &registrationRequest); err != nil {
			return invalidRequestEnvelope(err)
		}
		registration, err := d.management.Register(context.Background(), registrationRequest)
		if err != nil {
			return errorEnvelope("management_registration_failed", "plugin management registration failed", false, http.StatusInternalServerError)
		}
		return successEnvelope(registration)
	case pluginabi.MethodManagementHandle:
		if d == nil || d.management == nil {
			return errorEnvelope("management_unavailable", "plugin management is unavailable", false, http.StatusServiceUnavailable)
		}
		var managementRequest ManagementRequest
		if err := decodeRequest(request, &managementRequest); err != nil {
			return invalidRequestEnvelope(err)
		}
		managementResponse, err := d.management.Handle(context.Background(), managementRequest)
		if err != nil {
			return errorEnvelope("management_request_failed", "plugin management request failed", false, http.StatusInternalServerError)
		}
		return successEnvelope(managementResponse)
	default:
		return errorEnvelope("unknown_method", "unknown plugin method", false, http.StatusNotFound)
	}
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type lifecycleRequest struct {
	SchemaVersion uint32 `json:"schema_version"`
}

func validateLifecycleRequest(raw []byte) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var request lifecycleRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return fmt.Errorf("plugin lifecycle request is invalid")
	}
	if request.SchemaVersion > pluginabi.SchemaVersion {
		return fmt.Errorf("plugin schema version %d is not supported", request.SchemaVersion)
	}
	return nil
}

func decodeRequest(raw []byte, dst any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte(`{}`)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("request JSON is invalid")
	}
	return nil
}

func invalidRequestEnvelope(err error) []byte {
	return errorEnvelope("invalid_request", err.Error(), false, http.StatusBadRequest)
}
