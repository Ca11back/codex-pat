package plugin

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	Name          = "Codex PAT"
	Author        = "Ca11back"
	RepositoryURL = "https://github.com/Ca11back/codex-pat"
)

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

type capabilities struct {
	AuthProvider  bool `json:"auth_provider"`
	ManagementAPI bool `json:"management_api"`
}

func newRegistration(version string) registration {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             Name,
			Version:          version,
			Author:           Author,
			GitHubRepository: RepositoryURL,
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: capabilities{
			AuthProvider:  true,
			ManagementAPI: true,
		},
	}
}
