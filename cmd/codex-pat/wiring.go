package main

import (
	"oaipat/internal/hostrpc"
	"oaipat/internal/management"
	"oaipat/internal/pat"
	pluginlogic "oaipat/internal/plugin"
)

func newDispatcher(host hostrpc.API, version string) *pluginlogic.Dispatcher {
	service := pat.NewService(host, version)
	handler := management.New(host, service)
	return pluginlogic.NewDispatcher(version, handler)
}
