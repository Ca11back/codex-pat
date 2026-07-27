package pat

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func ParseAuth(request pluginapi.AuthParseRequest) pluginapi.AuthParseResponse {
	credential, owned, err := DecodeOwnedCredential(request.RawJSON)
	if !owned {
		if err != nil && IsOwnedFile(request.FileName) {
			return pluginapi.AuthParseResponse{
				Handled: true,
				Auth:    disabledAuthData(request.RawJSON, request.FileName, request.Path),
			}
		}
		return pluginapi.AuthParseResponse{}
	}
	if err != nil || credential.Validate() != nil {
		return pluginapi.AuthParseResponse{
			Handled: true,
			Auth:    disabledAuthData(request.RawJSON, request.FileName, request.Path),
		}
	}
	if IsOwnedFile(request.FileName) && !IsOwnedFileForPrincipal(request.FileName, credential.AccountID, credential.ChatGPTUserID) {
		return pluginapi.AuthParseResponse{
			Handled: true,
			Auth:    disabledAuthData(request.RawJSON, request.FileName, request.Path),
		}
	}
	raw := append([]byte(nil), bytes.TrimSpace(request.RawJSON)...)
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    credential.AuthData(raw, request.FileName, request.Path),
	}
}

func RefreshAuth(request pluginapi.AuthRefreshRequest) pluginapi.AuthRefreshResponse {
	response := ParseAuth(pluginapi.AuthParseRequest{
		Provider: request.AuthProvider,
		RawJSON:  request.StorageJSON,
		Host:     request.Host,
	})
	if response.Handled {
		return pluginapi.AuthRefreshResponse{Auth: response.Auth}
	}
	provider := strings.TrimSpace(request.AuthProvider)
	if provider == "" {
		provider = Provider
	}
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		ID:          strings.TrimSpace(request.AuthID),
		Provider:    provider,
		Disabled:    metadataBool(request.Metadata, "disabled"),
		StorageJSON: append([]byte(nil), request.StorageJSON...),
		Metadata:    cloneAnyMap(request.Metadata),
		Attributes:  cloneStringMap(request.Attributes),
	}}
}

func metadataBool(metadata map[string]any, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
