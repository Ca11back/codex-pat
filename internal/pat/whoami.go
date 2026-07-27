package pat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	DefaultAuthAPIBaseURL = "https://auth.openai.com/api/accounts"
	WhoamiPath            = "/v1/user-auth-credential/whoami"
	MaxTokenBytes         = 16 * 1024
	MaxWhoamiBodyBytes    = 1024 * 1024
	MaxIdentityFieldBytes = 4 * 1024
	originator            = "codex_cli_rs"
)

type HTTPDoer interface {
	HTTPDoLimited(context.Context, string, pluginapi.HTTPRequest, int) (pluginapi.HTTPResponse, error)
}

type Identity struct {
	UserID    string
	AccountID string
	PlanType  string
	Email     string
	FedRAMP   bool
}

func NormalizeToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", newError(ErrorInvalidInput, "PAT is required", http.StatusBadRequest, false, nil)
	}
	if len(token) > MaxTokenBytes {
		return "", newError(ErrorInvalidInput, "PAT exceeds the allowed size", http.StatusRequestEntityTooLarge, false, nil)
	}
	if !strings.HasPrefix(token, "at-") {
		return "", newError(ErrorInvalidInput, "credential is not a Codex PAT", http.StatusBadRequest, false, nil)
	}
	for _, value := range token {
		if value < 0x20 || value == 0x7f {
			return "", newError(ErrorInvalidInput, "PAT contains invalid control characters", http.StatusBadRequest, false, nil)
		}
	}
	return token, nil
}

func ResolveWhoamiURL(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultAuthAPIBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse Codex Auth API base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("Codex Auth API base URL must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Codex Auth API base URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + WhoamiPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func FetchIdentity(ctx context.Context, client HTTPDoer, callbackID, baseURL, version, rawToken string) (Identity, error) {
	if client == nil {
		return Identity{}, newError(ErrorTransient, "PAT validation transport is unavailable", http.StatusServiceUnavailable, true, nil)
	}
	token, err := NormalizeToken(rawToken)
	if err != nil {
		return Identity{}, err
	}
	endpoint, err := ResolveWhoamiURL(baseURL)
	if err != nil {
		return Identity{}, newError(ErrorInvalidInput, "Codex Auth API base URL is invalid", http.StatusInternalServerError, false, err)
	}
	request := pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    endpoint,
		Headers: http.Header{
			"Accept":        {"application/json"},
			"Authorization": {"Bearer " + token},
			"Originator":    {originator},
			"User-Agent":    {"codex-pat/" + safeVersion(version)},
		},
	}
	response, err := client.HTTPDoLimited(ctx, callbackID, request, MaxWhoamiBodyBytes)
	if err != nil {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			if response.StatusCode != 0 {
				return Identity{}, classifyStatus(response.StatusCode)
			}
		}
		if responseBodyTooLarge(err) {
			return Identity{}, newError(ErrorMalformedResponse, "Codex Auth API returned oversized account metadata", http.StatusBadGateway, false, err)
		}
		return Identity{}, newError(ErrorTransient, "PAT validation request failed", http.StatusBadGateway, true, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Identity{}, classifyStatus(response.StatusCode)
	}
	identity, err := decodeIdentity(response.Body)
	if err != nil {
		return Identity{}, newError(ErrorMalformedResponse, "Codex Auth API returned invalid account metadata", http.StatusBadGateway, false, err)
	}
	return identity, nil
}

func classifyStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return newError(ErrorAuthentication, "PAT was rejected by the Codex Auth API", status, false, nil)
	case http.StatusTooManyRequests:
		return newError(ErrorRateLimited, "Codex Auth API rate limited PAT validation", status, true, nil)
	default:
		if status >= http.StatusInternalServerError {
			return newError(ErrorTransient, "Codex Auth API is temporarily unavailable", status, true, nil)
		}
		return newError(ErrorRejected, "Codex Auth API rejected PAT validation", status, false, nil)
	}
}

func decodeIdentity(raw []byte) (Identity, error) {
	if len(raw) == 0 || len(raw) > MaxWhoamiBodyBytes {
		return Identity{}, fmt.Errorf("whoami response size is invalid")
	}
	var wire struct {
		UserID    *string `json:"chatgpt_user_id"`
		AccountID *string `json:"chatgpt_account_id"`
		PlanType  *string `json:"chatgpt_plan_type"`
		FedRAMP   *bool   `json:"chatgpt_account_is_fedramp"`
		Email     *string `json:"email"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&wire); err != nil {
		return Identity{}, fmt.Errorf("decode whoami response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Identity{}, fmt.Errorf("whoami response contains trailing JSON")
		}
		return Identity{}, fmt.Errorf("decode trailing whoami data: %w", err)
	}
	if wire.UserID == nil || wire.AccountID == nil || wire.PlanType == nil || wire.FedRAMP == nil {
		return Identity{}, fmt.Errorf("whoami response is missing required fields")
	}
	userID, err := normalizeIdentityField("chatgpt_user_id", *wire.UserID, true)
	if err != nil {
		return Identity{}, err
	}
	accountID, err := normalizeIdentityField("chatgpt_account_id", *wire.AccountID, true)
	if err != nil {
		return Identity{}, err
	}
	planType, err := normalizeIdentityField("chatgpt_plan_type", *wire.PlanType, true)
	if err != nil {
		return Identity{}, err
	}
	email := ""
	if wire.Email != nil {
		email, err = normalizeIdentityField("email", *wire.Email, false)
		if err != nil {
			return Identity{}, err
		}
	}
	identity := Identity{
		UserID:    userID,
		AccountID: accountID,
		PlanType:  planType,
		Email:     email,
		FedRAMP:   *wire.FedRAMP,
	}
	return identity, nil
}

func normalizeIdentityField(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("whoami response field %s is empty", name)
		}
		return "", nil
	}
	if len(value) > MaxIdentityFieldBytes {
		return "", fmt.Errorf("whoami response field %s is too large", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", fmt.Errorf("whoami response field %s contains control characters", name)
		}
	}
	return value, nil
}

func responseBodyTooLarge(err error) bool {
	var target interface {
		error
		ResponseBodyTooLarge() bool
	}
	return errors.As(err, &target) && target.ResponseBodyTooLarge()
}

func safeVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return -1
		}
		return r
	}, version)
	if version == "" {
		return "dev"
	}
	return version
}
