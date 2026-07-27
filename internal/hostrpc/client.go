package hostrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// RawCaller invokes a CPA host callback and returns its JSON envelope.
// The native ABI adapter owns all C buffer allocation and release.
type RawCaller func(method string, request []byte) ([]byte, error)

// API is the host functionality used by PAT and management services.
type API interface {
	HTTPDo(context.Context, string, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
	HTTPDoLimited(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error)
	AuthList(context.Context, string) ([]pluginapi.HostAuthFileEntry, error)
	AuthGet(context.Context, string, string) (pluginapi.HostAuthGetResponse, error)
	AuthGetRuntime(context.Context, string, string) (pluginapi.HostAuthGetRuntimeResponse, error)
	AuthSave(context.Context, string, string, json.RawMessage) (pluginapi.HostAuthSaveResponse, error)
	Log(context.Context, string, string, map[string]any) error
}

// ResponseBodyLimitError reports that a streamed host response exceeded the
// caller's configured bound. It intentionally carries no upstream body data.
type ResponseBodyLimitError struct {
	Limit int
}

func (e *ResponseBodyLimitError) Error() string {
	return fmt.Sprintf("host HTTP response exceeds %d bytes", e.Limit)
}

func (e *ResponseBodyLimitError) ResponseBodyTooLarge() bool {
	return true
}

// Client provides typed wrappers around CPA's JSON host callbacks.
type Client struct {
	call RawCaller
}

func New(call RawCaller) *Client {
	return &Client{call: call}
}

func (c *Client) HTTPDo(ctx context.Context, callbackID string, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	payload := hostHTTPRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		Method:         req.Method,
		URL:            req.URL,
		Headers:        cloneHeader(req.Headers),
		Body:           append([]byte(nil), req.Body...),
	}
	var response pluginapi.HTTPResponse
	if err := c.invoke(ctx, pluginabi.MethodHostHTTPDo, payload, &response); err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	response.Headers = cloneHeader(response.Headers)
	response.Body = append([]byte(nil), response.Body...)
	return response, nil
}

// HTTPDoLimited uses CPA's streaming host bridge so an upstream response is
// bounded before the complete body crosses the native plugin ABI.
func (c *Client) HTTPDoLimited(ctx context.Context, callbackID string, req pluginapi.HTTPRequest, maxBodyBytes int) (pluginapi.HTTPResponse, error) {
	if maxBodyBytes <= 0 {
		return pluginapi.HTTPResponse{}, fmt.Errorf("host HTTP response limit must be positive")
	}
	payload := hostHTTPRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		Method:         req.Method,
		URL:            req.URL,
		Headers:        cloneHeader(req.Headers),
		Body:           append([]byte(nil), req.Body...),
	}
	var opened hostHTTPStreamResponse
	if err := c.invoke(ctx, pluginabi.MethodHostHTTPDoStream, payload, &opened); err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	streamID := strings.TrimSpace(opened.StreamID)
	if streamID == "" {
		return pluginapi.HTTPResponse{}, fmt.Errorf("host HTTP stream returned no identifier")
	}

	closed := false
	defer func() {
		if !closed {
			_ = c.closeHTTPStream(context.Background(), streamID)
		}
	}()

	response := pluginapi.HTTPResponse{
		StatusCode: opened.StatusCode,
		Headers:    cloneHeader(opened.Headers),
	}
	body := make([]byte, 0, min(maxBodyBytes, 32*1024))
	for {
		var chunk hostHTTPStreamReadResponse
		if err := c.invoke(ctx, pluginabi.MethodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: streamID}, &chunk); err != nil {
			return response, err
		}
		if chunk.Error != "" {
			return response, fmt.Errorf("host HTTP response stream failed")
		}
		if len(chunk.Payload) > maxBodyBytes-len(body) {
			return response, &ResponseBodyLimitError{Limit: maxBodyBytes}
		}
		body = append(body, chunk.Payload...)
		if chunk.Done {
			break
		}
	}
	if err := c.closeHTTPStream(context.Background(), streamID); err != nil {
		return response, err
	}
	closed = true
	response.Body = body
	return response, nil
}

func (c *Client) closeHTTPStream(ctx context.Context, streamID string) error {
	return c.invoke(ctx, pluginabi.MethodHostHTTPStreamClose, hostHTTPStreamCloseRequest{StreamID: streamID}, nil)
}

func (c *Client) AuthList(ctx context.Context, callbackID string) ([]pluginapi.HostAuthFileEntry, error) {
	var response struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	request := struct {
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}{HostCallbackID: strings.TrimSpace(callbackID)}
	if err := c.invoke(ctx, pluginabi.MethodHostAuthList, request, &response); err != nil {
		return nil, err
	}
	return append([]pluginapi.HostAuthFileEntry(nil), response.Files...), nil
}

func (c *Client) AuthGet(ctx context.Context, callbackID, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	var response pluginapi.HostAuthGetResponse
	request := hostAuthGetRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		AuthIndex:      strings.TrimSpace(authIndex),
	}
	if err := c.invoke(ctx, pluginabi.MethodHostAuthGet, request, &response); err != nil {
		return pluginapi.HostAuthGetResponse{}, err
	}
	response.JSON = append(json.RawMessage(nil), response.JSON...)
	return response, nil
}

func (c *Client) AuthGetRuntime(ctx context.Context, callbackID, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	var response pluginapi.HostAuthGetRuntimeResponse
	request := hostAuthGetRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		AuthIndex:      strings.TrimSpace(authIndex),
	}
	if err := c.invoke(ctx, pluginabi.MethodHostAuthGetRuntime, request, &response); err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	return response, nil
}

func (c *Client) AuthSave(ctx context.Context, callbackID, name string, raw json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	var response pluginapi.HostAuthSaveResponse
	request := hostAuthSaveRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		Name:           strings.TrimSpace(name),
		JSON:           append(json.RawMessage(nil), raw...),
	}
	if err := c.invoke(ctx, pluginabi.MethodHostAuthSave, request, &response); err != nil {
		return pluginapi.HostAuthSaveResponse{}, err
	}
	return response, nil
}

func (c *Client) Log(ctx context.Context, callbackID, level string, fields map[string]any) error {
	request := hostLogRequest{
		HostCallbackID: strings.TrimSpace(callbackID),
		Level:          strings.TrimSpace(level),
		Message:        "codex-pat",
		Fields:         cloneFields(fields),
	}
	return c.invoke(ctx, pluginabi.MethodHostLog, request, nil)
}

func (c *Client) invoke(ctx context.Context, method string, request, response any) error {
	if c == nil || c.call == nil {
		return fmt.Errorf("host callback bridge is unavailable")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal host callback request %s: %w", method, err)
	}
	rawResponse, err := c.call(method, rawRequest)
	if err != nil {
		return fmt.Errorf("call host callback %s: %w", method, err)
	}
	return decodeEnvelope(method, rawResponse, response)
}

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type hostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

type hostAuthGetRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	AuthIndex      string `json:"auth_index"`
}

type hostAuthSaveRequest struct {
	HostCallbackID string          `json:"host_callback_id,omitempty"`
	Name           string          `json:"name"`
	JSON           json.RawMessage `json:"json"`
}

func cloneHeader(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func cloneFields(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
