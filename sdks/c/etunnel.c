/*
 * etunnel SDK for C (dependencies: libwebsockets >= 4, libcurl, json-c).
 * Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
 *
 * Build:
 *   apt install libwebsockets-dev libcurl4-openssl-dev libjson-c-dev
 *   gcc etunnel.c -o etunnel -lwebsockets -lcurl -ljson-c
 * Run:
 *   ETUNNEL_SERVER=wss://t.ggcode.uk/tunnel ./etunnel 3000
 */
#include <libwebsockets.h>
#include <curl/curl.h>
#include <json-c/json.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <signal.h>
#include <ctype.h>

#define MAXP 32768

static int g_local_port = 3000;
static volatile sig_atomic_t g_stop = 0;
static pthread_mutex_t g_send_mtx = PTHREAD_MUTEX_INITIALIZER;

typedef struct {
    char method[16];
    char path[2048];
    unsigned char *body;
    size_t body_len;
} stream_buf_t;

#define MAX_STREAMS 128
static stream_buf_t g_streams[MAX_STREAMS]; /* indexed by sid % MAX_STREAMS (sids are small+odd) */
static pthread_mutex_t g_streams_mtx = PTHREAD_MUTEX_INITIALIZER;

typedef struct {
    struct lws *wsi;
} session_t;
static session_t g_sess;

/* ---- wire frames ------------------------------------------------------------ */

static int send_ws(struct lws *wsi, unsigned char *data, size_t len, int is_text) {
    unsigned char buf[LWS_PRE + 65536];
    memcpy(buf + LWS_PRE, data, len);
    pthread_mutex_lock(&g_send_mtx);
    int n = lws_write(wsi, buf + LWS_PRE, len,
                      is_text ? LWS_WRITE_TEXT : LWS_WRITE_BINARY);
    pthread_mutex_unlock(&g_send_mtx);
    return n;
}

static int send_text(struct lws *wsi, const char *json) {
    return send_ws(wsi, (unsigned char *) json, strlen(json), 1);
}

static int send_frame(struct lws *wsi, unsigned char kind, uint32_t sid,
                      const unsigned char *payload, size_t n) {
    unsigned char f[9 + MAXP];
    f[0] = kind;
    f[1] = sid >> 24; f[2] = sid >> 16; f[3] = sid >> 8; f[4] = sid & 0xFF;
    f[5] = (n >> 24) & 0xFF; f[6] = (n >> 16) & 0xFF; f[7] = (n >> 8) & 0xFF; f[8] = n & 0xFF;
    if (n) memcpy(f + 9, payload, n);
    return send_ws(wsi, f, 9 + n, 0);
}

/* ---- local HTTP via curl ------------------------------------------------------ */

typedef struct {
    char *data;
    size_t len;
} grow_t;

static size_t grow_cb(char *p, size_t s, size_t n, void *u) {
    grow_t *g = u;
    g->data = realloc(g->data, g->len + s * n + 1);
    memcpy(g->data + g->len, p, s * n);
    g->len += s * n;
    return s * n;
}

static size_t header_cb(char *p, size_t s, size_t n, void *u) {
    return grow_cb(p, s, n, u);
}

typedef struct {
    struct lws *wsi;
    uint32_t sid;
    char method[16];
    char path[2048];
    unsigned char *body;
    size_t body_len;
    int local_port;
} serve_arg_t;

static void *serve_thread(void *arg) {
    serve_arg_t *a = arg;
    CURL *curl = curl_easy_init();
    grow_t hdr = {0}, body = {0};
    long status = 502;

    char url[2300];
    snprintf(url, sizeof(url), "http://127.0.0.1:%d%s", a->local_port, a->path);
    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_CUSTOMREQUEST, a->method);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 120L);
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, header_cb);
    curl_easy_setopt(curl, CURLOPT_HEADERDATA, &hdr);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, grow_cb);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &body);
    if (a->body_len && strcmp(a->method, "GET") != 0 && strcmp(a->method, "HEAD") != 0) {
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, a->body);
        curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long) a->body_len);
    }
    if (curl_easy_perform(curl) == CURLE_OK)
        curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &status);
    curl_easy_cleanup(curl);

    /* res json with headers from raw header block */
    json_object *hdrs = json_object_new_object();
    char *h = hdr.data ? hdr.data : "";
    char *save = NULL;
    for (char *line = strtok_r(h, "\r\n", &save); line; line = strtok_r(NULL, "\r\n", &save)) {
        char *colon = strchr(line, ':');
        if (!colon) continue;
        *colon = 0;
        char *v = colon + 1;
        while (*v == ' ') v++;
        for (char *c = line; *c; c++) *c = tolower(*c);
        if (!strcmp(line, "content-length") || !strcmp(line, "connection")
            || !strcmp(line, "transfer-encoding") || !strcmp(line, "keep-alive")) continue;
        json_object *arr = json_object_new_array();
        json_object_array_add(arr, json_object_new_string(v));
        json_object_object_add(hdrs, line, arr);
    }
    json_object *res = json_object_new_object();
    json_object_object_add(res, "type", json_object_new_string("res"));
    json_object_object_add(res, "streamId", json_object_new_int((int) a->sid));
    json_object_object_add(res, "status", json_object_new_int((int) status));
    json_object_object_add(res, "headers", hdrs);
    const char *j = json_object_to_json_string(res);
    send_text(a->wsi, j);
    json_object_put(res);

    size_t i = 0;
    while (i < body.len) {
        size_t n = body.len - i > MAXP ? MAXP : body.len - i;
        send_frame(a->wsi, 0x01, a->sid, body.data + i, n);
        i += n;
    }
    send_frame(a->wsi, 0x02, a->sid, NULL, 0);

    free(hdr.data); free(body.data); free(a->body); free(a);
    return NULL;
}

/* ---- libwebsockets ------------------------------------------------------------ */

static int ws_cb(struct lws *wsi, enum lws_callback_reasons reason,
                 void *user, void *in, size_t len) {
    switch (reason) {
    case LWS_CALLBACK_CLIENT_ESTABLISHED: {
        char hello[512];
        const char *tok = getenv("ETUNNEL_TOKEN");
        const char *sub = getenv("ETUNNEL_SUBDOMAIN");
        int n = snprintf(hello, sizeof(hello),
            "{\"type\":\"hello\",\"proto\":1%s%s,\"client\":\"etunnel-c/1.0\"}",
            sub && *sub ? ",\"subdomain\":\"" : "",
            0);
        (void) n;
        snprintf(hello, sizeof(hello), "{\"type\":\"hello\",\"proto\":1%s%s%s%s%s%s,\"client\":\"etunnel-c/1.0\"}",
            sub && *sub ? ",\"subdomain\":\"" : "", sub && *sub ? sub : "", sub && *sub ? "\"" : "",
            tok && *tok ? ",\"token\":\"" : "", tok && *tok ? tok : "", tok && *tok ? "\"" : "");
        send_text(wsi, hello);
        break;
    }
    case LWS_CALLBACK_CLIENT_RECEIVE: {
        if (lws_frame_is_binary(wsi)) {
            unsigned char *b = in;
            if (len < 9) break;
            unsigned char kind = b[0];
            uint32_t sid = ((uint32_t) b[1] << 24) | ((uint32_t) b[2] << 16)
                         | ((uint32_t) b[3] << 8) | b[4];
            uint32_t n = ((uint32_t) b[5] << 24) | ((uint32_t) b[6] << 16)
                       | ((uint32_t) b[7] << 8) | b[8];
            pthread_mutex_lock(&g_streams_mtx);
            stream_buf_t *st = &g_streams[sid % MAX_STREAMS];
            if (st->method[0]) {
                if (kind == 0x01) {
                    size_t take = n < len - 9 ? n : len - 9;
                    st->body = realloc(st->body, st->body_len + take);
                    memcpy(st->body + st->body_len, b + 9, take);
                    st->body_len += take;
                } else if (kind == 0x02) {
                    serve_arg_t *a = calloc(1, sizeof(*a));
                    a->wsi = wsi;
                    a->sid = sid;
                    memcpy(a->method, st->method, sizeof(a->method));
                    memcpy(a->path, st->path, sizeof(a->path));
                    a->body = st->body;
                    a->body_len = st->body_len;
                    a->local_port = g_local_port;
                    st->body = NULL; st->body_len = 0;
                    pthread_t th;
                    pthread_create(&th, NULL, serve_thread, a);
                    pthread_detach(th);
                } else if (kind == 0x03) {
                    free(st->body); st->body = NULL; st->body_len = 0;
                    st->method[0] = 0;
                }
            }
            pthread_mutex_unlock(&g_streams_mtx);
        } else {
            char *t = strndup(in, len);
            json_object *m = json_tokener_parse(t);
            if (m) {
                const char *type = json_object_get_string(json_object_object_get(m, "type"));
                if (type && !strcmp(type, "welcome")) {
                    printf("TUNNEL_URL: %s\n",
                        json_object_get_string(json_object_object_get(m, "url")));
                    fflush(stdout);
                } else if (type && !strcmp(type, "req")) {
                    uint32_t sid = (uint32_t) json_object_get_int(json_object_object_get(m, "streamId"));
                    pthread_mutex_lock(&g_streams_mtx);
                    stream_buf_t *st = &g_streams[sid % MAX_STREAMS];
                    snprintf(st->method, sizeof(st->method), "%s",
                        json_object_get_string(json_object_object_get(m, "method")) ?: "GET");
                    snprintf(st->path, sizeof(st->path), "%s",
                        json_object_get_string(json_object_object_get(m, "path")) ?: "/");
                    free(st->body); st->body = NULL; st->body_len = 0;
                    pthread_mutex_unlock(&g_streams_mtx);
                } else if (type && !strcmp(type, "ping")) {
                    char pong[64];
                    snprintf(pong, sizeof(pong), "{\"type\":\"pong\",\"ts\":%lld}",
                        (long long) (json_object_get_int64(json_object_object_get(m, "ts"))));
                    send_text(wsi, pong);
                } else if (type && !strcmp(type, "bye")) {
                    g_stop = 1;
                }
                json_object_put(m);
            }
            free(t);
        }
        break;
    }
    case LWS_CALLBACK_CLIENT_CONNECTION_ERROR:
        fprintf(stderr, "etunnel: connection error: %s\n", (char *) in);
        g_stop = 1;
        break;
    case LWS_CALLBACK_CLIENT_CLOSED:
        g_stop = 1;
        break;
    default:
        break;
    }
    return 0;
}

static const struct lws_protocols protocols[] = {
    {"etunnel", ws_cb, 0, 128 * 1024, 0, NULL, 0},
    {NULL, NULL, 0, 0, 0, NULL, 0}
};

static void on_sigint(int sig) { (void) sig; g_stop = 1; }

int main(int argc, char **argv) {
    signal(SIGINT, on_sigint);
    if (argc > 1) g_local_port = atoi(argv[1]);
    else if (getenv("ETUNNEL_LOCAL")) g_local_port = atoi(getenv("ETUNNEL_LOCAL"));
    const char *server = getenv("ETUNNEL_SERVER") ?: "wss://t.ggcode.uk/tunnel";

    lws_set_log_level(0, NULL);
    struct lws_context_creation_info info;
    memset(&info, 0, sizeof(info));
    info.port = CONTEXT_PORT_NO_LISTEN;
    info.protocols = protocols;
    info.options = LWS_SERVER_OPTION_DO_SSL_GLOBAL_INIT;
    info.fd_limit_per_thread = 1 + 1 + 1;
    struct lws_context *ctx = lws_create_context(&info);
    if (!ctx) return 1;

    /* parse wss://host/path */
    char host[256] = {0}, path[256] = "/tunnel";
    int port = 443, ssl = 1;
    const char *hp = strstr(server, "://");
    if (hp) {
        hp += 3;
        const char *slash = strchr(hp, '/');
        if (slash) {
            memcpy(host, hp, slash - hp < 255 ? slash - hp : 255);
            snprintf(path, sizeof(path), "%s", slash);
        } else {
            snprintf(host, sizeof(host), "%s", hp);
        }
        if (!strncmp(server, "ws://", 5)) { port = 80; ssl = 0; }
    }

    struct lws_client_connect_info cc;
    memset(&cc, 0, sizeof(cc));
    cc.context = ctx;
    cc.address = host;
    cc.port = port;
    cc.path = path;
    cc.host = host;
    cc.origin = host;
    cc.protocol = "etunnel-v1";
    if (ssl) cc.ssl_connection = LCCSCF_USE_SSL;

    struct lws *wsi = lws_client_connect_via_info(&cc);
    if (!wsi) return 1;

    while (!g_stop) {
        lws_service(ctx, 100);
        lws_callback_on_writable(wsi);
    }
    lws_context_destroy(ctx);
    return 0;
}
