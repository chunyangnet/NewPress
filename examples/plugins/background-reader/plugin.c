/* Apache-2.0. Source-only example, no signed package is provided.
 * Build with a WASM-capable clang or zig cc (see README.md).
 * Background execution intentionally has no mutable or outbound side effects.
 */
typedef unsigned int u32;
typedef unsigned long long u64;
#define EXPORT(name) __attribute__((export_name(name)))
__attribute__((import_module("newpress"), import_name("host_call")))
extern u32 host_call(u32, u32, u32, u32);
static char input[65536], reply[65536];
static const char request[] = "{\"method\":\"settings.get\"}";
static const char result[] = "{\"data\":{\"processed\":true}}";
EXPORT("np_api_version") u64 api_version(void) { return 1; }
EXPORT("np_alloc") u32 alloc(u32 n) { return n <= sizeof(input) ? (u32)input : 0; }
EXPORT("np_free") void release(u32 p, u32 n) { (void)p; (void)n; }
EXPORT("np_handle") u64 handle(u32 p, u32 n) {
    if (p != (u32)input || n > sizeof(input)) __builtin_trap();
    u32 size = host_call((u32)request, sizeof(request)-1, (u32)reply, sizeof(reply));
    if (!size || size > sizeof(reply)) __builtin_trap();
    /* The v1 host uses encoding/json on its response map: the top-level
     * "ok" field is last. Check the complete suffix, never a substring in
     * settings data. Fail closed if the host changes its wire encoding. */
    static const char success[] = ",\"ok\":true}";
    if (size < sizeof(success)-1) __builtin_trap();
    for (u32 i = 0; i < sizeof(success)-1; i++) {
        if (reply[size-(sizeof(success)-1)+i] != success[i]) __builtin_trap();
    }
    return ((u64)(u32)result << 32) | (sizeof(result)-1);
}
