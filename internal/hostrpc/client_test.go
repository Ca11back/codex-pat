package hostrpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestClientHTTPDoForwardsCallbackContext(t *testing.T) {
	client := New(func(method string, request []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("method = %q", method)
		}
		var payload hostHTTPRequest
		if err := json.Unmarshal(request, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.HostCallbackID != "callback-1" {
			t.Fatalf("callback id = %q", payload.HostCallbackID)
		}
		if payload.Headers.Get("Authorization") != "Bearer fake" {
			t.Fatalf("authorization header = %q", payload.Headers.Get("Authorization"))
		}
		return successEnvelope(t, pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"ok":true}`),
		}), nil
	})

	response, err := client.HTTPDo(context.Background(), "callback-1", pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     "https://example.invalid/whoami",
		Headers: http.Header{"Authorization": {"Bearer fake"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"ok":true}` {
		t.Fatalf("response = %#v", response)
	}
}

func TestClientHTTPDoLimitedStreamsAndCloses(t *testing.T) {
	readCount := 0
	closed := false
	client := New(func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			var payload hostHTTPRequest
			if err := json.Unmarshal(request, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.HostCallbackID != "callback-stream" {
				t.Fatalf("callback id = %q", payload.HostCallbackID)
			}
			return successEnvelope(t, hostHTTPStreamResponse{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"Content-Type": {"application/json"}},
				StreamID:   "stream-1",
			}), nil
		case pluginabi.MethodHostHTTPStreamRead:
			readCount++
			if readCount == 1 {
				return successEnvelope(t, hostHTTPStreamReadResponse{Payload: []byte(`{"ok":`)}), nil
			}
			return successEnvelope(t, hostHTTPStreamReadResponse{Payload: []byte(`true}`), Done: true}), nil
		case pluginabi.MethodHostHTTPStreamClose:
			closed = true
			return successEnvelope(t, struct{}{}), nil
		default:
			t.Fatalf("unexpected method %q", method)
			return nil, nil
		}
	})

	response, err := client.HTTPDoLimited(context.Background(), "callback-stream", pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    "https://example.invalid/whoami",
	}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` || response.StatusCode != http.StatusOK {
		t.Fatalf("response = %#v", response)
	}
	if !closed || readCount != 2 {
		t.Fatalf("closed=%t readCount=%d", closed, readCount)
	}
}

func TestClientHTTPDoLimitedRejectsOversizedBodyAndCloses(t *testing.T) {
	closed := false
	client := New(func(method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			return successEnvelope(t, hostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-2"}), nil
		case pluginabi.MethodHostHTTPStreamRead:
			return successEnvelope(t, hostHTTPStreamReadResponse{Payload: []byte("12345")}), nil
		case pluginabi.MethodHostHTTPStreamClose:
			closed = true
			return successEnvelope(t, struct{}{}), nil
		default:
			t.Fatalf("unexpected method %q", method)
			return nil, nil
		}
	})

	_, err := client.HTTPDoLimited(context.Background(), "", pluginapi.HTTPRequest{URL: "https://example.invalid"}, 4)
	var limitErr *ResponseBodyLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v", err)
	}
	if !closed {
		t.Fatal("oversized stream was not closed")
	}
}

func TestClientReturnsStructuredRPCError(t *testing.T) {
	client := New(func(string, []byte) ([]byte, error) {
		return json.Marshal(pluginabi.Envelope{
			OK: false,
			Error: &pluginabi.Error{
				Code:       "host_call_failed",
				Message:    "upstream unavailable",
				Retryable:  true,
				HTTPStatus: http.StatusBadGateway,
			},
		})
	})

	_, err := client.AuthList(context.Background(), "")
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %v", err)
	}
	if !rpcErr.Retryable || rpcErr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("rpc error = %#v", rpcErr)
	}
}

func TestClientRejectsMalformedEnvelope(t *testing.T) {
	client := New(func(string, []byte) ([]byte, error) { return []byte(`not-json`), nil })
	if _, err := client.AuthList(context.Background(), ""); err == nil {
		t.Fatal("expected malformed envelope error")
	}
}

func successEnvelope(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
