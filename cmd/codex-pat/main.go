package main

import (
	"sync"

	"oaipat/internal/hostrpc"
	pluginlogic "oaipat/internal/plugin"
)

var version = "dev"

var application struct {
	sync.RWMutex
	dispatcher *pluginlogic.Dispatcher
}

func main() {}

func initializePlugin() {
	application.Lock()
	defer application.Unlock()
	if application.dispatcher != nil {
		return
	}
	host := hostrpc.New(rawHostCall)
	application.dispatcher = newDispatcher(host, version)
}

func dispatchPlugin(method string, request []byte) []byte {
	application.RLock()
	dispatcher := application.dispatcher
	application.RUnlock()
	if dispatcher == nil {
		initializePlugin()
		application.RLock()
		dispatcher = application.dispatcher
		application.RUnlock()
	}
	if dispatcher == nil {
		return pluginlogic.ErrorResponse("plugin_unavailable", "plugin is unavailable")
	}
	return dispatcher.Dispatch(method, request)
}

func shutdownPlugin() {
	application.Lock()
	application.dispatcher = nil
	application.Unlock()
}
