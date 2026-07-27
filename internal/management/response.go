package management

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func jsonDataResponse(status int, data any) pluginapi.ManagementResponse {
	return jsonResponse(status, dataEnvelope{Data: data})
}

func jsonErrorResponse(status int, code, message string, retryable bool) pluginapi.ManagementResponse {
	return jsonResponse(status, errorEnvelope{Error: errorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	}})
}

func jsonResponse(status int, value any) pluginapi.ManagementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		body = []byte(`{"error":{"code":"internal_error","message":"The response could not be encoded.","retryable":false}}`)
		status = http.StatusInternalServerError
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":           []string{"application/json; charset=utf-8"},
			"Cache-Control":          []string{"no-store"},
			"X-Content-Type-Options": []string{"nosniff"},
		},
		Body: body,
	}
}
