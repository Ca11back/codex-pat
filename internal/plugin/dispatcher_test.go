package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"oaipat/internal/pat"
)

func TestRegistrationAdvertisesOnlyRequiredCapabilities(t *testing.T) {
	dispatcher := NewDispatcher("0.1.0", fakeManagement{})
	envelope := decodeTestEnvelope(t, dispatcher.Dispatch(pluginabi.MethodPluginRegister, []byte(`{"schema_version":1}`)))
	if !envelope.OK {
		t.Fatalf("registration error = %#v", envelope.Error)
	}
	var raw map[string]any
	if err := json.Unmarshal(envelope.Result, &raw); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := raw["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", raw["capabilities"])
	}
	if len(capabilities) != 2 || capabilities["auth_provider"] != true || capabilities["management_api"] != true {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	metadata, ok := raw["metadata"].(map[string]any)
	if !ok || metadata["Name"] != Name || metadata["Version"] != "0.1.0" ||
		metadata["Author"] != Author || metadata["GitHubRepository"] != RepositoryURL {
		t.Fatalf("metadata = %#v", raw["metadata"])
	}
}

func TestAuthIdentifierIsCodex(t *testing.T) {
	dispatcher := NewDispatcher("test", fakeManagement{})
	envelope := decodeTestEnvelope(t, dispatcher.Dispatch(pluginabi.MethodAuthIdentifier, nil))
	var response identifierResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.Identifier != pat.Provider {
		t.Fatalf("identifier = %q", response.Identifier)
	}
}

func TestUnsupportedLoginAndUnknownMethodReturnStableErrors(t *testing.T) {
	dispatcher := NewDispatcher("test", fakeManagement{})
	for _, method := range []string{pluginabi.MethodAuthLoginStart, "not.a.method"} {
		envelope := decodeTestEnvelope(t, dispatcher.Dispatch(method, nil))
		if envelope.OK || envelope.Error == nil || envelope.Error.Code == "" {
			t.Fatalf("method %s envelope = %#v", method, envelope)
		}
	}
}

func TestManagementCallbackIDIsForwarded(t *testing.T) {
	management := &recordingManagement{}
	dispatcher := NewDispatcher("test", management)
	request := []byte(`{"Method":"GET","Path":"/status","host_callback_id":"callback-7"}`)
	envelope := decodeTestEnvelope(t, dispatcher.Dispatch(pluginabi.MethodManagementHandle, request))
	if !envelope.OK {
		t.Fatalf("management error = %#v", envelope.Error)
	}
	if management.callbackID != "callback-7" {
		t.Fatalf("callback id = %q", management.callbackID)
	}
}

func decodeTestEnvelope(t *testing.T, raw []byte) pluginabi.Envelope {
	t.Helper()
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

type fakeManagement struct{}

func (fakeManagement) Register(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{}, nil
}

func (fakeManagement) Handle(context.Context, ManagementRequest) (pluginapi.ManagementResponse, error) {
	return pluginapi.ManagementResponse{}, nil
}

type recordingManagement struct {
	callbackID string
}

func (r *recordingManagement) Register(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{}, nil
}

func (r *recordingManagement) Handle(_ context.Context, request ManagementRequest) (pluginapi.ManagementResponse, error) {
	r.callbackID = request.HostCallbackID
	return pluginapi.ManagementResponse{}, nil
}
