#include "abi.h"
#include "_cgo_export.h"
#include <stdlib.h>

#if defined(__APPLE__) && defined(__x86_64__)
#include <pthread.h>

/* Intel macOS Go runtimes share the same goroutine TLS slot. Each crossing
 * into the other runtime must start on a fresh native thread. */
static void run_on_native_thread(void *(*run)(void *), void *request) {
    pthread_t thread;
    if (pthread_create(&thread, NULL, run, request) != 0) {
        return;
    }
    /* The worker uses storage owned by its caller until it has exited. */
    if (pthread_join(thread, NULL) != 0) {
        abort();
    }
}

struct init_request {
    const cliproxy_host_api *host;
    uint32_t version;
};

static void *run_init(void *arg) {
    struct init_request *request = arg;
    request->version = codexPatInit((cliproxy_host_api *)request->host);
    return NULL;
}

struct call_request {
    const char *method;
    const uint8_t *body;
    size_t len;
    cliproxy_buffer *response;
    int result;
};

static void *run_plugin_call(void *arg) {
    struct call_request *request = arg;
    request->result = cliproxyPluginCall((char *)request->method, (uint8_t *)request->body,
                                        request->len, request->response);
    return NULL;
}

static void *run_shutdown(void *unused) {
    (void)unused;
    cliproxyPluginShutdown();
    return NULL;
}

static void isolated_shutdown(void) {
    run_on_native_thread(run_shutdown, NULL);
}

static void isolated_plugin_free(void *ptr, size_t len) {
    (void)len;
    free(ptr);
}

struct host_call_request {
    const cliproxy_host_api *host;
    struct call_request call;
};

static void *run_host_call(void *arg) {
    struct host_call_request *request = arg;
    const cliproxy_host_api *host = request->host;
    struct call_request *call = &request->call;
    call->result = host->call(host->host_ctx, call->method, call->body, call->len, call->response);
    return NULL;
}

struct host_free_request {
    const cliproxy_host_api *host;
    void *ptr;
    size_t len;
};

static void *run_host_free(void *arg) {
    struct host_free_request *request = arg;
    request->host->free_buffer(request->ptr, request->len);
    return NULL;
}
#endif

static int call_plugin(const char *method, const uint8_t *body, size_t len, cliproxy_buffer *response) {
#if defined(__APPLE__) && defined(__x86_64__)
    struct call_request request = {method, body, len, response, 1};
    run_on_native_thread(run_plugin_call, &request);
    return request.result;
#else
    return cliproxyPluginCall((char *)method, (uint8_t *)body, len, response);
#endif
}

int cliproxy_plugin_init(const cliproxy_host_api *host, cliproxy_plugin_api *plugin) {
    if (plugin == NULL) {
        return 1;
    }
#if defined(__APPLE__) && defined(__x86_64__)
    struct init_request request = {host, 0};
    run_on_native_thread(run_init, &request);
    uint32_t version = request.version;
#else
    uint32_t version = codexPatInit((cliproxy_host_api *)host);
#endif
    if (version == 0) {
        return 1;
    }
    plugin->abi_version = version;
    plugin->call = call_plugin;
#if defined(__APPLE__) && defined(__x86_64__)
    plugin->free_buffer = isolated_plugin_free;
    plugin->shutdown = isolated_shutdown;
#else
    plugin->free_buffer = cliproxyPluginFree;
    plugin->shutdown = cliproxyPluginShutdown;
#endif
    return 0;
}

int call_host_api(const cliproxy_host_api *host, const char *method, const uint8_t *body,
                  size_t len, cliproxy_buffer *response) {
#if defined(__APPLE__) && defined(__x86_64__)
    struct host_call_request request = {host, {method, body, len, response, 1}};
    run_on_native_thread(run_host_call, &request);
    return request.call.result;
#else
    return host->call(host->host_ctx, method, body, len, response);
#endif
}

void free_host_buffer(const cliproxy_host_api *host, void *ptr, size_t len) {
#if defined(__APPLE__) && defined(__x86_64__)
    struct host_free_request request = {host, ptr, len};
    run_on_native_thread(run_host_free, &request);
#else
    host->free_buffer(ptr, len);
#endif
}
