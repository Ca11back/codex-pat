package plugin

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"oaipat/internal/pat"
)

func TestRegistrationAdvertisesOnlyRequiredCapabilities(t *testing.T) {
	dispatcher := NewDispatcher("0.1.0", fakeManagement{})
	envelope := decodeTestEnvelope(t, dispatcher.Dispatch(pluginabi.MethodPluginRegister, []byte(`{"schema_version":2}`)))
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
	if raw["schema_version"] != float64(pluginSchemaVersion) {
		t.Fatalf("schema_version = %#v, want %d", raw["schema_version"], pluginSchemaVersion)
	}
	metadata, ok := raw["metadata"].(map[string]any)
	if !ok || metadata["Name"] != Name || metadata["Version"] != "0.1.0" ||
		metadata["Author"] != Author || metadata["GitHubRepository"] != RepositoryURL {
		t.Fatalf("metadata = %#v", raw["metadata"])
	}
}

func TestCPAContractVersions(t *testing.T) {
	if pluginabi.ABIVersion != 1 {
		t.Fatalf("ABI version = %d, want 1", pluginabi.ABIVersion)
	}
	if pluginabi.SchemaVersion != 3 {
		t.Fatalf("SDK schema version = %d, want 3", pluginabi.SchemaVersion)
	}
	if pluginSchemaVersion != 2 {
		t.Fatalf("PAT plugin schema version = %d, want 2", pluginSchemaVersion)
	}
	if pluginSchemaVersion == pluginabi.SchemaVersion {
		t.Fatal("PAT plugin schema must not be derived from the SDK schema version")
	}
}

func TestLifecycleMethodsAcceptVerifiedHostSchemasAndReturnPluginSchemaTwo(t *testing.T) {
	dispatcher := NewDispatcher("test", fakeManagement{})
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		for hostSchema := minHostSchemaVersion; hostSchema <= maxHostSchemaVersion; hostSchema++ {
			t.Run(method+"/host-schema-"+strconv.FormatUint(uint64(hostSchema), 10), func(t *testing.T) {
				request := []byte(`{"schema_version":` + strconv.FormatUint(uint64(hostSchema), 10) + `}`)
				envelope := decodeTestEnvelope(t, dispatcher.Dispatch(method, request))
				if !envelope.OK || envelope.Error != nil {
					t.Fatalf("envelope = %#v", envelope)
				}
				var registration registration
				if err := json.Unmarshal(envelope.Result, &registration); err != nil {
					t.Fatal(err)
				}
				if registration.SchemaVersion != pluginSchemaVersion {
					t.Fatalf("response schema version = %d, want %d", registration.SchemaVersion, pluginSchemaVersion)
				}
			})
		}
	}
}

func TestLifecycleMethodsRejectInvalidOrUnsupportedSchemas(t *testing.T) {
	dispatcher := NewDispatcher("test", fakeManagement{})
	tests := []struct {
		name    string
		request string
		code    string
		message string
	}{
		{name: "schema v1", request: `{"schema_version":1}`, code: "unsupported_schema", message: "host plugin schema version 1 is not supported"},
		{name: "missing schema", request: `{}`, code: "unsupported_schema", message: "host plugin schema version 0 is not supported"},
		{name: "zero schema", request: `{"schema_version":0}`, code: "unsupported_schema", message: "host plugin schema version 0 is not supported"},
		{name: "empty request", request: ``, code: "unsupported_schema", message: "host plugin schema version 0 is not supported"},
		{name: "malformed JSON", request: `{"schema_version":`, code: "invalid_request", message: "plugin lifecycle request is invalid"},
		{name: "future schema", request: `{"schema_version":4}`, code: "unsupported_schema", message: "host plugin schema version 4 is not supported"},
		{name: "far future schema", request: `{"schema_version":99}`, code: "unsupported_schema", message: "host plugin schema version 99 is not supported"},
	}
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		method := method
		for _, test := range tests {
			test := test
			t.Run(method+"/"+test.name, func(t *testing.T) {
				envelope := decodeTestEnvelope(t, dispatcher.Dispatch(method, []byte(test.request)))
				if envelope.OK || envelope.Error == nil {
					t.Fatalf("envelope = %#v", envelope)
				}
				if envelope.Error.Code != test.code || envelope.Error.Message != test.message ||
					envelope.Error.Retryable || envelope.Error.HTTPStatus != 400 {
					t.Fatalf("error = %#v", envelope.Error)
				}
				if len(envelope.Result) != 0 {
					t.Fatalf("unexpected result = %s", envelope.Result)
				}
			})
		}
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
