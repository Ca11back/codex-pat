package plugin

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func successEnvelope(value any) []byte {
	result, err := json.Marshal(value)
	if err != nil {
		return errorEnvelope("encode_failed", "plugin response could not be encoded", false, 0)
	}
	raw, err := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"encode_failed","message":"plugin response could not be encoded"}}`)
	}
	return raw
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			Retryable:  retryable,
			HTTPStatus: status,
		},
	})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin request failed"}}`)
	}
	return raw
}

// ErrorResponse creates a stable plugin ABI error envelope without exposing an
// internal failure or panic value.
func ErrorResponse(code, message string) []byte {
	return errorEnvelope(code, message, false, 0)
}
