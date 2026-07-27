package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <limits.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static int call_host_api(const cliproxy_host_api* host, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (host == NULL || host->call == NULL) {
		return 1;
	}
	return host->call(host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(const cliproxy_host_api* host, void* ptr, size_t len) {
	if (host != NULL && host->free_buffer != NULL && ptr != NULL) {
		host->free_buffer(ptr, len);
	}
}

static int size_fits_go_bytes(size_t len) {
	return len <= INT_MAX;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"

	pluginlogic "oaipat/internal/plugin"
)

var hostState struct {
	sync.RWMutex
	api *C.cliproxy_host_api
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) (code C.int) {
	defer func() {
		if recover() != nil {
			clearHostAPI()
			code = 1
		}
	}()
	if api == nil || !hostAPICompatible(host) {
		return 1
	}
	storeHostAPI(host)
	api.abi_version = C.uint32_t(pluginabi.ABIVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (code C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			if response.ptr != nil {
				C.free(response.ptr)
				response.ptr = nil
				response.len = 0
			}
			if !writeResponse(response, pluginlogic.ErrorResponse("plugin_panic", "plugin request failed")) {
				code = 1
			}
		}
	}()
	if method == nil {
		if !writeResponse(response, pluginlogic.ErrorResponse("invalid_method", "plugin method is required")) {
			return 1
		}
		return 0
	}
	var requestBytes []byte
	if requestLen > 0 && (request == nil || C.size_fits_go_bytes(requestLen) == 0) {
		if !writeResponse(response, pluginlogic.ErrorResponse("invalid_request", "plugin request is invalid")) {
			return 1
		}
		return 0
	}
	if requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw := dispatchPlugin(C.GoString(method), requestBytes)
	if !writeResponse(response, raw) {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownPlugin()
	clearHostAPI()
}

func rawHostCall(method string, request []byte) ([]byte, error) {
	methodPtr := C.CString(method)
	if methodPtr == nil {
		return nil, fmt.Errorf("allocate host callback method")
	}
	defer C.free(unsafe.Pointer(methodPtr))

	var requestPtr unsafe.Pointer
	if len(request) > 0 {
		requestPtr = C.CBytes(request)
		if requestPtr == nil {
			return nil, fmt.Errorf("allocate host callback request")
		}
		defer C.free(requestPtr)
	}
	hostState.RLock()
	defer hostState.RUnlock()
	host := hostState.api
	if host == nil {
		return nil, fmt.Errorf("host callback is unavailable")
	}
	var response C.cliproxy_buffer
	callCode := C.call_host_api(host, methodPtr, (*C.uint8_t)(requestPtr), C.size_t(len(request)), &response)
	var raw []byte
	if response.ptr != nil && response.len > 0 {
		if C.size_fits_go_bytes(response.len) != 0 {
			raw = C.GoBytes(response.ptr, C.int(response.len))
		}
	}
	if response.ptr != nil {
		C.free_host_buffer(host, response.ptr, response.len)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback failed")
	}
	if response.len > 0 && len(raw) == 0 {
		return nil, fmt.Errorf("host callback response is too large")
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("host callback returned no response")
	}
	return raw, nil
}

func hostAPICompatible(host *C.cliproxy_host_api) bool {
	return host != nil &&
		host.abi_version == C.uint32_t(pluginabi.ABIVersion) &&
		host.call != nil &&
		host.free_buffer != nil
}

func storeHostAPI(host *C.cliproxy_host_api) {
	hostState.Lock()
	hostState.api = host
	hostState.Unlock()
}

func clearHostAPI() {
	hostState.Lock()
	hostState.api = nil
	hostState.Unlock()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) bool {
	if response == nil || len(raw) == 0 {
		return false
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
	return true
}
