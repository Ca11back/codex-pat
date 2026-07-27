package management

import (
	"embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed assets/index.html assets/app.css assets/app.js assets/icons/*.svg assets/LICENSE.lucide.txt
var embeddedAssets embed.FS

type asset struct {
	file        string
	contentType string
	csp         string
}

var assetsByPath = map[string]asset{
	resourceManagePath: {
		file:        "assets/index.html",
		contentType: "text/html; charset=utf-8",
		csp: "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
			"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
	},
	resourceCSSPath: {
		file:        "assets/app.css",
		contentType: "text/css; charset=utf-8",
	},
	resourceJSPath: {
		file:        "assets/app.js",
		contentType: "text/javascript; charset=utf-8",
	},
	resourceRefreshIconPath: {
		file:        "assets/icons/refresh-cw.svg",
		contentType: "image/svg+xml",
	},
	resourceTrashIconPath: {
		file:        "assets/icons/trash-2.svg",
		contentType: "image/svg+xml",
	},
	resourceKeyIconPath: {
		file:        "assets/icons/key-round.svg",
		contentType: "image/svg+xml",
	},
}

func resourceResponse(path string) (pluginapi.ManagementResponse, bool) {
	item, ok := assetsByPath[path]
	if !ok {
		return pluginapi.ManagementResponse{}, false
	}
	body, err := embeddedAssets.ReadFile(item.file)
	if err != nil {
		return jsonErrorResponse(http.StatusInternalServerError, "asset_unavailable", "The resource is unavailable.", false), true
	}
	headers := http.Header{
		"Content-Type":           []string{item.contentType},
		"Cache-Control":          []string{"no-cache"},
		"X-Content-Type-Options": []string{"nosniff"},
		"Referrer-Policy":        []string{"no-referrer"},
	}
	if item.csp != "" {
		headers.Set("Content-Security-Policy", item.csp)
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    headers,
		Body:       append([]byte(nil), body...),
	}, true
}
