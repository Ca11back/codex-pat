#ifndef CODEX_PAT_ABI_H
#define CODEX_PAT_ABI_H

#include <stddef.h>
#include <stdint.h>

typedef struct {
    void *ptr;
    size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void *, const char *, const uint8_t *, size_t, cliproxy_buffer *);
typedef void (*cliproxy_host_free_fn)(void *, size_t);

typedef struct {
    uint32_t abi_version;
    void *host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char *, const uint8_t *, size_t, cliproxy_buffer *);
typedef void (*cliproxy_plugin_free_fn)(void *, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

#if defined(_WIN32)
__declspec(dllexport)
#endif
int cliproxy_plugin_init(const cliproxy_host_api *, cliproxy_plugin_api *);
int call_host_api(const cliproxy_host_api *, const char *, const uint8_t *, size_t, cliproxy_buffer *);
void free_host_buffer(const cliproxy_host_api *, void *, size_t);

#endif
