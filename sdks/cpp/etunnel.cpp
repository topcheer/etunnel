// etunnel SDK for C++17 (dependencies: websocketpp + asio/standalone, or any
// WebSocket client; HTTP via libcurl). Buffer-based protocol client (bodies
// buffered per stream; no WS upgrades).
//
// Build:
//   apt install libwebsocketpp-dev libasio-dev libcurl4-openssl-dev
//   g++ -std=c++17 etunnel.cpp -lcurl -o etunnel
// Run:
//   ETUNNEL_SERVER=wss://t.etun.uk/tunnel ./etunnel 3000
// Env: ETUNNEL_SERVER, ETUNNEL_LOCAL, ETUNNEL_TOKEN, ETUNNEL_SUBDOMAIN;
//      ETUNNEL_RECONNECT=0 disables the reconnect supervisor.
#include <websocketpp/config/asio_client.hpp>
#include <websocketpp/client.hpp>
#include <openssl/x509v3.h>
#include <openssl/x509_vfy.h>
#include <curl/curl.h>
#include <algorithm>
#include <atomic>
#include <csignal>
#include <cstdint>
#include <cstdlib>
#include <ctime>
#include <iostream>
#include <map>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>
#include <chrono>

using WsClient = websocketpp::client<websocketpp::config::asio_tls_client>;
using WsMessagePtr = WsClient::message_ptr;
using JsonHeaders = std::map<std::string, std::vector<std::string>>;

static const size_t MAXP = 32768;

// ---- CLI supervisor -----------------------------------------------------------

// Set by SIGINT/SIGTERM only: the tunnel loop must not reconnect after it.
static volatile sig_atomic_t g_stopped = 0;
static std::string g_resume; // server-signed token: reclaim the subdomain on reconnect
static void on_signal(int sig) { (void) sig; g_stopped = 1; }

// ---- tiny JSON helpers (flat messages) ---------------------------------------

static std::string json_escape(const std::string &s) {
    std::string out;
    for (char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            default: out += c;
        }
    }
    return out;
}

static std::string json_str_field(const std::string &j, const std::string &key) {
    auto pat = "\"" + key + "\":";
    auto i = j.find(pat);
    // Only accept a field boundary: the byte before the opening quote must be
    // '{', ',' or string start, so a value that embeds the literal text
    // `"key":` cannot hijack the lookup (#49 LOW).
    while (i != std::string::npos && i > 0 && j[i - 1] != '{' && j[i - 1] != ',')
        i = j.find(pat, i + 1);
    if (i == std::string::npos) return "";
    i = j.find('"', i + pat.size());
    if (i == std::string::npos) return "";
    i++;
    std::string out;
    while (i < j.size() && j[i] != '"') {
        if (j[i] == '\\' && i + 1 < j.size()) {
            char n = j[i + 1];
            if (n == 'n') out += '\n';
            else if (n == 't') out += '\t';
            else out += n;
            i += 2;
        } else out += j[i++];
    }
    return out;
}

static long json_num_field(const std::string &j, const std::string &key) {
    auto pat = "\"" + key + "\":";
    auto i = j.find(pat);
    if (i == std::string::npos) return 0;
    i += pat.size();
    return strtol(j.c_str() + i, nullptr, 10);
}

// ---- wire frames ---------------------------------------------------------------

static std::vector<uint8_t> frame(uint8_t kind, uint32_t sid, const std::string &payload) {
    std::vector<uint8_t> f(9 + payload.size());
    f[0] = kind;
    f[1] = sid >> 24; f[2] = sid >> 16; f[3] = sid >> 8; f[4] = sid;
    uint32_t n = payload.size();
    f[5] = n >> 24; f[6] = n >> 16; f[7] = n >> 8; f[8] = n;
    std::copy(payload.begin(), payload.end(), f.begin() + 9);
    return f;
}

// ---- local HTTP via curl --------------------------------------------------------

struct StreamBuf {
    std::string method, path;
    std::string body;
};

// Themed 502 page shown when the local app is down (same HTML as the other SDKs).
// Error text is truncated to 256 bytes to keep the frame payload bounded.
static std::string localErrorPage(const char *msg) {
    std::string m = msg ? msg : "error";
    if (m.size() > 256) m.resize(256);
    std::string esc;
    esc.reserve(m.size());
    for (char c : m) {
        if (c == '&') esc += "&amp;";
        else if (c == '<') esc += "&lt;";
        else if (c == '>') esc += "&gt;";
        else esc += c;
    }
    return std::string(R"html(<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - local app down</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#f85149}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#58a6ff;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class="badge">502 local app down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class="panel">
<div class="k">error</div><div class="v err">)html") + esc +
        R"html(</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>)html";
}

static void serve(std::shared_ptr<WsClient> client, websocketpp::connection_hdl hdl,
                  const std::string &method, const std::string &path,
                  const std::string &body, uint32_t sid, int local_port) {
    CURL *curl = curl_easy_init();
    if (!curl) return;
    std::string url = "http://127.0.0.1:" + std::to_string(local_port) + path;
    std::string respHeaders, respBody;
    curl_easy_setopt(curl, CURLOPT_URL, url.c_str());
    curl_easy_setopt(curl, CURLOPT_CUSTOMREQUEST, method.c_str());
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 120L);
    if (!body.empty() && method != "GET" && method != "HEAD") {
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, body.c_str());
        curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long) body.size());
    }
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, +[](char *p, size_t s, size_t n, void *u) -> size_t {
        ((std::string *) u)->append(p, s * n); return s * n;
    });
    curl_easy_setopt(curl, CURLOPT_HEADERDATA, &respHeaders);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, +[](char *p, size_t s, size_t n, void *u) -> size_t {
        ((std::string *) u)->append(p, s * n); return s * n;
    });
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &respBody);

    long status = 502;
    CURLcode rc = curl_easy_perform(curl);
    if (rc == CURLE_OK) {
        curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &status);
    } else {
        status = 502;
        respBody = localErrorPage(curl_easy_strerror(rc));
        respHeaders = "Content-Type: text/html; charset=utf-8";
    }
    curl_easy_cleanup(curl);

    // re-serialize response headers
    std::string hdrJson;
    size_t pos = 0;
    while (pos < respHeaders.size()) {
        size_t eol = respHeaders.find("\r\n", pos);
        if (eol == std::string::npos) eol = respHeaders.size();
        std::string line = respHeaders.substr(pos, eol - pos);
        pos = eol + 2;
        auto colon = line.find(':');
        if (colon == std::string::npos) continue;
        std::string k = line.substr(0, colon);
        std::string v = line.substr(colon + 1);
        while (!v.empty() && (v.front() == ' ')) v.erase(0, 1);
        std::string lk = k;
        for (auto &c : lk) c = tolower(c);
        if (lk == "content-length" || lk == "connection" || lk == "transfer-encoding"
            || lk == "keep-alive") continue;
        if (!hdrJson.empty()) hdrJson += ",";
        hdrJson += "\"" + json_escape(k) + "\":[\"" + json_escape(v) + "\"]";
    }

    // Send the response back. The connection may have died while curl was
    // running (up to 120s): every send takes an error_code and the first
    // failure stops the sequence - a stale hdl yields an error instead of a
    // use-after-free (#49).
    auto send_ok = [&](const void *p, size_t n, websocketpp::frame::opcode::value op) -> bool {
        websocketpp::lib::error_code ec;
        client->send(hdl, p, n, op, ec);
        return !ec;
    };
    std::string res = "{\"type\":\"res\",\"streamId\":" + std::to_string(sid)
        + ",\"status\":" + std::to_string(status) + ",\"headers\":{" + hdrJson + "}}";
    if (!send_ok(res.data(), res.size(), websocketpp::frame::opcode::text)) return;
    for (size_t i = 0; i < respBody.size(); i += MAXP) {
        size_t n = std::min(MAXP, respBody.size() - i);
        auto f = frame(0x01, sid, respBody.substr(i, n));
        if (!send_ok(f.data(), f.size(), websocketpp::frame::opcode::binary)) return;
    }
    auto fin = frame(0x02, sid, "");
    send_ok(fin.data(), fin.size(), websocketpp::frame::opcode::binary);
}

int main(int argc, char **argv) {
    const char *serverEnv = getenv("ETUNNEL_SERVER");
    #ifndef ETUNNEL_DEFAULT_SERVER
#define ETUNNEL_DEFAULT_SERVER "wss://t.etun.uk/tunnel"
#endif
    std::string server = serverEnv ? serverEnv : ETUNNEL_DEFAULT_SERVER;
    const char *localEnv = getenv("ETUNNEL_LOCAL");
    int local_port = 3000;
    if (argc > 1) local_port = atoi(argv[1]);
    else if (localEnv) local_port = atoi(localEnv);

    // supervisor: connect, wait for the disconnect, back off, retry. The message
    // handler prints TUNNEL_URL on every welcome frame, so a reconnect
    // re-announces the URL. ETUNNEL_RECONNECT=0 restores the old behaviour.
    const char *reconnectEnv = getenv("ETUNNEL_RECONNECT");
    const bool reconnect = !(reconnectEnv && std::string(reconnectEnv) == "0");
    long backoff_ms = 500;
    std::srand((unsigned) std::time(nullptr));

    while (!g_stopped) {
        // The serve threads share ownership: curl can still be running when
        // the connection drops, so the client must outlive the loop body -
        // a stack object here was a use-after-free (#49).
        auto client = std::make_shared<WsClient>();
        client->init_asio();
        // Host pinning for TLS identity (#49/#65): verify_peer alone proves the
        // chain is valid, not that it belongs to the host we dialed - a MITM
        // with any CA-signed certificate for ANY domain passes. The expected
        // hostname goes into the verify param, so the handshake fails on
        // SAN/CN mismatch the same way curl does. An unparsable URL must not
        // silently downgrade to chain-only verification.
        std::string tls_host;
        {
            std::string u = server;
            auto scheme = u.find("://");
            if (scheme != std::string::npos) {
                auto host_start = scheme + 3;
                auto host_end = u.find_first_of("/", host_start);
                if (host_end == std::string::npos) host_end = u.size();
                tls_host = u.substr(host_start, host_end - host_start);
                auto colon = tls_host.rfind(':');
                if (colon != std::string::npos) tls_host.resize(colon);
            }
        }
        if (tls_host.empty()) {
            std::cerr << "etunnel: cannot determine TLS hostname from " << server << std::endl;
            return 1;
        }

        client->set_tls_init_handler([tls_host](websocketpp::connection_hdl) {
            auto ctx = websocketpp::lib::make_shared<websocketpp::lib::asio::ssl::context>(
                websocketpp::lib::asio::ssl::context::tlsv12);
            // Asio/OpenSSL default to verify_none: without verify_peer a wss://
            // connection accepts ANY certificate and the token plus all tunnel
            // traffic are exposed to an active MITM (#49 HIGH). Verify against
            // the system trust store by default - chain AND hostname; the only
            // escape hatch is an explicit, loud env switch that disables BOTH.
            const char *insecure = getenv("ETUNNEL_INSECURE_TLS");
            if (insecure && std::string(insecure) == "1") {
                std::cerr << "etunnel: WARNING ETUNNEL_INSECURE_TLS=1 - certificate chain "
                             "AND hostname verification disabled (MITM exposure)" << std::endl;
                ctx->set_verify_mode(websocketpp::lib::asio::ssl::context::verify_none);
            } else {
                ctx->set_verify_mode(websocketpp::lib::asio::ssl::context::verify_peer);
                ctx->set_default_verify_paths();
                SSL_CTX *ssl = ctx->native_handle();
                // Optional extra trust root for internal CAs (#49); also what
                // the hostname regression test uses to control the chain.
                // Native OpenSSL call: websocketpp's load_verify_file wrapper
                // does not compile against the Boost.Asio in noble 1.83
                // (binds a non-const error_code& from an rvalue).
                if (const char *ca = getenv("ETUNNEL_CA_FILE"); ca && *ca) {
                    if (SSL_CTX_load_verify_locations(ssl, ca, nullptr) != 1) {
                        std::cerr << "etunnel: ETUNNEL_CA_FILE (" << ca
                                  << "): invalid or unreadable" << std::endl;
                    }
                }
                X509_VERIFY_PARAM *param = SSL_CTX_get0_param(ssl);
                // set1_host compares a DNS name - an IP literal would never
                // match an IP SAN and every dial to 127.0.0.1 would fail.
                // Pick the matcher the way curl does (curl uses the right
                // one depending on the URL host).
                bool pinned;
                if (tls_host.find_first_not_of("0123456789.:abcdefABCDEF") == std::string::npos &&
                    tls_host.find_first_of(".:") != std::string::npos) {
                    pinned = X509_VERIFY_PARAM_set1_ip_asc(param, tls_host.c_str()) == 1;
                } else {
                    pinned = X509_VERIFY_PARAM_set1_host(param, tls_host.c_str(), tls_host.size()) == 1;
                }
                if (!pinned) {
                    // Fail closed: never continue with chain-only verification.
                    std::cerr << "etunnel: TLS hostname pin failed; refusing to connect" << std::endl;
                    ctx->set_verify_mode(websocketpp::lib::asio::ssl::context::verify_none);
                    std::cerr << "etunnel: TLS verification refused" << std::endl;
                }
            }
            return ctx;
        });

        std::mutex mtx;
        std::map<uint32_t, StreamBuf> streams;
        std::atomic<bool> gotUrl{false};
        std::atomic<bool> closed{false};

        // Outstanding serve threads. Finished ones are pruned on the next
        // spawn; the rest are joined at the end of the iteration, before the
        // supervisor moves on (#49).
        struct Worker {
            std::thread th;
            std::atomic<bool> done{false};
        };
        std::mutex wmtx;
        std::vector<std::unique_ptr<Worker>> workers;

        client->set_message_handler([&](websocketpp::connection_hdl hdl, WsMessagePtr msg) {
            std::lock_guard<std::mutex> lk(mtx);
            if (msg->get_opcode() == websocketpp::frame::opcode::text) {
                std::string t = msg->get_payload();
                if (t.find("\"type\":\"welcome\"") != std::string::npos) {
                    g_resume = json_str_field(t, "resumeToken");
                    std::cout << "TUNNEL_URL: " << json_str_field(t, "url") << std::endl;
                    gotUrl = true;
                } else if (t.find("\"type\":\"req\"") != std::string::npos) {
                    uint32_t sid = (uint32_t) json_num_field(t, "streamId");
                    streams[sid] = {json_str_field(t, "method"), json_str_field(t, "path"), ""};
                } else if (t.find("\"type\":\"ping\"") != std::string::npos) {
                    long ts = json_num_field(t, "ts");
                    websocketpp::lib::error_code ec;
                    client->send(hdl, "{\"type\":\"pong\",\"ts\":" + std::to_string(ts) + "}",
                                 websocketpp::frame::opcode::text, ec);
                }
            } else if (msg->get_opcode() == websocketpp::frame::opcode::binary) {
                const std::string &b = msg->get_payload();
                if (b.size() < 9) return;
                uint8_t kind = b[0];
                uint32_t sid = ((uint32_t)(uint8_t) b[1] << 24) | ((uint32_t)(uint8_t) b[2] << 16)
                    | ((uint32_t)(uint8_t) b[3] << 8) | (uint8_t) b[4];
                uint32_t n = ((uint32_t)(uint8_t) b[5] << 24) | ((uint32_t)(uint8_t) b[6] << 16)
                    | ((uint32_t)(uint8_t) b[7] << 8) | (uint8_t) b[8];
                auto it = streams.find(sid);
                if (it == streams.end()) return;
                if (kind == 0x01) {
                    it->second.body.append(b, 9, std::min((size_t) n, b.size() - 9));
                } else if (kind == 0x02) {
                    StreamBuf st = it->second;
                    streams.erase(it);
                    auto w = std::make_unique<Worker>();
                    Worker *wr = w.get();
                    // Value-capture the shared_ptr: the thread keeps the client
                    // alive even if the connection drops mid-curl (#49).
                    w->th = std::thread([client, hdl, st = std::move(st), sid, local_port, wr]() {
                        serve(client, hdl, st.method, st.path, st.body, sid, local_port);
                        wr->done = true;
                    });
                    std::lock_guard<std::mutex> wl(wmtx);
                    workers.erase(std::remove_if(workers.begin(), workers.end(),
                        [](std::unique_ptr<Worker> &p) {
                            if (p->done.load()) {
                                if (p->th.joinable()) p->th.join();
                                return true;
                            }
                            return false;
                        }), workers.end());
                    workers.push_back(std::move(w));
                } else if (kind == 0x03) {
                    streams.erase(it);
                }
            }
        });
        client->set_open_handler([&](websocketpp::connection_hdl hdl) {
            websocketpp::lib::error_code ec;
            std::string token = getenv("ETUNNEL_TOKEN") ? getenv("ETUNNEL_TOKEN") : "";
            std::string sub = getenv("ETUNNEL_SUBDOMAIN") ? getenv("ETUNNEL_SUBDOMAIN") : "";
            std::string hello = "{\"type\":\"hello\",\"proto\":1";
            if (!sub.empty()) hello += ",\"subdomain\":\"" + json_escape(sub) + "\"";
            if (!token.empty()) hello += ",\"token\":\"" + json_escape(token) + "\"";
            if (!g_resume.empty()) hello += ",\"resume\":\"" + json_escape(g_resume) + "\"";
            hello += ",\"client\":\"etunnel-cpp/1.6.11\"}";
            client->send(hdl, hello, websocketpp::frame::opcode::text, ec);
        });
        // the supervisor's only view of the connection lifecycle
        client->set_fail_handler([&](websocketpp::connection_hdl) { closed = true; });
        client->set_close_handler([&](websocketpp::connection_hdl) { closed = true; });

        websocketpp::lib::error_code ec;
        auto con = client->get_connection(server, ec);
        if (ec) {
            std::cerr << "etunnel: " << ec.message() << std::endl;
            closed = true;
        } else {
            con->add_subprotocol("etunnel-v1");
            client->connect(con);
            // Drive the asio loop by hand instead of client.run(), so that a
            // signal can break out of a live connection.
            while (!g_stopped && !closed) {
                if (client->poll() == 0) {
                    std::this_thread::sleep_for(std::chrono::milliseconds(5));
                }
            }
        }
        client->stop();

        // Wait for every serve thread before the next iteration touches the
        // connection state they still reference (#49).
        {
            std::lock_guard<std::mutex> wl(wmtx);
            for (auto &p : workers)
                if (p->th.joinable()) p->th.join();
            workers.clear();
        }

        if (g_stopped) break;      // SIGINT/SIGTERM: no reconnect
        if (!reconnect) break;     // ETUNNEL_RECONNECT=0: no reconnect

        if (gotUrl) backoff_ms = 500;   // a successful connect resets the backoff

        long wait_ms = (long) (backoff_ms * (0.8 + 0.4 * ((double) rand() / (double) RAND_MAX)));
        std::cerr << "etunnel: disconnected, reconnecting in " << wait_ms << "ms" << std::endl;
        for (long slept = 0; slept < wait_ms && !g_stopped; ) {  // sliced: signal-responsive
            long slice = std::min<long>(100, wait_ms - slept);
            std::this_thread::sleep_for(std::chrono::milliseconds(slice));
            slept += slice;
        }
        backoff_ms = std::min<long>(backoff_ms * 2, 30000);
    }

    return 0;
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
//   et_credentials creds = et_login("https://t.etun.uk");  // opens the browser
//   // creds.api_token / creds.subdomains / creds.plan — persist and pass
//   // api_token as the token arg of future et_connect calls.
struct et_credentials {
    std::string api_token;
    std::vector<std::string> subdomains;
    std::string plan;
};

static size_t et_curl_write(char *p, size_t sz, size_t n, void *ud) {
    (static_cast<std::string *>(ud))->append(p, sz * n);
    return sz * n;
}

static bool et_post_json(const std::string &url, const std::string &body, std::string &out) {
    CURL *h = curl_easy_init();
    if (!h) return false;
    std::string buf;
    curl_slist *hdrs = nullptr;
    hdrs = curl_slist_append(hdrs, "Content-Type: application/json");
    curl_easy_setopt(h, CURLOPT_URL, url.c_str());
    curl_easy_setopt(h, CURLOPT_HTTPHEADER, hdrs);
    curl_easy_setopt(h, CURLOPT_POSTFIELDS, body.c_str());
    curl_easy_setopt(h, CURLOPT_WRITEFUNCTION, et_curl_write);
    curl_easy_setopt(h, CURLOPT_WRITEDATA, &buf);
    curl_easy_setopt(h, CURLOPT_TIMEOUT, 30L);
    CURLcode rc = curl_easy_perform(h);
    long code = 0;
    curl_easy_getinfo(h, CURLINFO_RESPONSE_CODE, &code);
    curl_slist_free_all(hdrs);
    curl_easy_cleanup(h);
    if (rc != CURLE_OK || code != 200) {
        std::cerr << "etunnel: login: HTTP " << code << " from " << url << ": "
                  << buf.substr(0, 200) << "\n";
        return false;
    }
    out = std::move(buf);
    return true;
}

static std::vector<std::string> et_json_array(const std::string &j, const std::string &key) {
    std::vector<std::string> out;
    std::string pat = "\"" + key + "\":[";
    size_t i = j.find(pat);
    if (i == std::string::npos) return out;
    size_t e = j.find(']', i + pat.size());
    if (e == std::string::npos) return out;
    std::string inner = j.substr(i + pat.size(), e - i - pat.size());
    size_t pos = 0;
    while (pos < inner.size()) {
        size_t q1 = inner.find('"', pos);
        if (q1 == std::string::npos) break;
        size_t q2 = inner.find('"', q1 + 1);
        if (q2 == std::string::npos) break;
        out.push_back(inner.substr(q1 + 1, q2 - q1 - 1));
        pos = q2 + 1;
    }
    return out;
}

et_credentials et_login(const std::string &server, std::function<void(const std::string &)> open_browser = {}, long timeout_secs = 600) {
    std::string base = server;
    while (!base.empty() && base.back() == '/') base.pop_back();
    if (base.empty()) throw std::runtime_error("etunnel: login: server required");

    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) throw std::runtime_error("etunnel: login: socket() failed");
    sockaddr_in a{};
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    a.sin_port = 0;
    int on = 1;
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &on, sizeof on);
    if (::bind(fd, (sockaddr *)&a, sizeof a) < 0 || ::listen(fd, 1) < 0) {
        ::close(fd);
        throw std::runtime_error("etunnel: login: bind/listen failed");
    }
    sockaddr_in bound{};
    socklen_t bl = sizeof bound;
    getsockname(fd, (sockaddr *)&bound, &bl);
    int port = ntohs(bound.sin_port);

    std::string resp;
    if (!et_post_json(base + "/auth/cli/start",
            "{\"redirect_uri\":\"http://127.0.0.1:" + std::to_string(port) + "/callback\"}", resp))
        { ::close(fd); throw std::runtime_error("etunnel: login: start failed"); }
    std::string auth_url = json_str_field(resp, "auth_url");
    std::string device_code = json_str_field(resp, "device_code");
    if (auth_url.empty() || device_code.empty()) {
        ::close(fd);
        throw std::runtime_error("etunnel: login: empty start payload");
    }

    bool opened = false;
    if (open_browser) { open_browser(auth_url); opened = true; }
    if (!opened) {
#ifdef __APPLE__
        std::string cmd = "open '" + auth_url + "' >/dev/null 2>&1 &";
#else
        std::string cmd = "xdg-open '" + auth_url + "' >/dev/null 2>&1 &";
#endif
        opened = (system(cmd.c_str()) == 0);
    }
    if (!opened) std::cerr << "etunnel: authorize in your browser: " << auth_url << "\n";

    std::string code;
    time_t deadline = time(nullptr) + timeout_secs;
    while (code.empty()) {
        if (time(nullptr) > deadline) { ::close(fd); throw std::runtime_error("etunnel: login timed out"); }
        timeval tv{1, 0};
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
        sockaddr_in ca{}; socklen_t cl = sizeof ca;
        int c = ::accept(fd, (sockaddr *)&ca, &cl);
        if (c < 0) continue;
        std::string req(4096, '\0');
        ssize_t n = recv(c, req.data(), req.size() - 1, 0);
        (void) n;
        size_t q = req.find(' ');
        if (q != std::string::npos) {
            size_t e = req.find(' ', q + 1);
            std::string path = req.substr(q + 1, (e == std::string::npos ? req.size() : e) - q - 1);
            size_t kv = path.find("code=");
            if (kv != std::string::npos) {
                size_t end = path.find('&', kv);
                code = path.substr(kv + 5, (end == std::string::npos ? path.size() : end) - kv - 5);
            }
        }
        std::string page = "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>";
        std::string hdr = "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: "
                        + std::to_string(page.size()) + "\r\nConnection: close\r\n\r\n";
        (void) ::send(c, (hdr + page).c_str(), hdr.size() + page.size(), 0);
        ::close(c);
    }
    ::close(fd);

    std::string ex;
    if (!et_post_json(base + "/auth/cli/exchange",
            "{\"device_code\":\"" + device_code + "\",\"code\":\"" + code + "\"}", ex))
        throw std::runtime_error("etunnel: login: exchange failed");
    et_credentials out;
    out.api_token = json_str_field(ex, "api_token");
    if (out.api_token.empty()) throw std::runtime_error("etunnel: login: no api_token in exchange response");
    out.subdomains = et_json_array(ex, "subdomains");
    out.plan = json_str_field(ex, "plan");
    return out;
}
