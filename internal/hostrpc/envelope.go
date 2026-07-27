package hostrpc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// RPCError is a redacted error returned by the CPA host callback bridge.
type RPCError struct {
	Code       string
	Message    string
	Retryable  bool
	HTTPStatus int
}

func (e *RPCError) Error() string {
	if e == nil {
		return "host callback failed"
	}
	code := strings.TrimSpace(e.Code)
	message := strings.TrimSpace(e.Message)
	switch {
	case code != "" && message != "":
		return code + ": " + message
	case code != "":
		return code
	case message != "":
		return message
	default:
		return "host callback failed"
	}
}

func decodeEnvelope(method string, raw []byte, dst any) error {
	if len(raw) == 0 {
		return fmt.Errorf("host callback %s returned no response", method)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode host callback envelope %s: %w", method, err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return &RPCError{Code: "host_call_failed", Message: "host callback failed"}
		}
		return &RPCError{
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			Retryable:  envelope.Error.Retryable,
			HTTPStatus: envelope.Error.HTTPStatus,
		}
	}
	if dst == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, dst); err != nil {
		return fmt.Errorf("decode host callback result %s: %w", method, err)
	}
	return nil
}
