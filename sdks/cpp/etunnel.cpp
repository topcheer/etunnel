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
#include <curl/curl.h>
#include <algorithm>
#include <atomic>
#include <csignal>
#include <cstdint>
#include <cstdlib>
#include <ctime>
#include <iostream>
#include <map>
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

static void serve(WsClient *client, websocketpp::connection_hdl hdl,
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
        std::string err;
        (void) err;
    } else {
        status = 502;
        respBody = "etunnel: local service error: " + std::string(curl_easy_strerror(rc)) + "\n";
        respHeaders = "Content-Type: text/plain";
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

    websocketpp::lib::error_code ec;
    std::string res = "{\"type\":\"res\",\"streamId\":" + std::to_string(sid)
        + ",\"status\":" + std::to_string(status) + ",\"headers\":{" + hdrJson + "}}";
    client->send(hdl, res, websocketpp::frame::opcode::text, ec);
    for (size_t i = 0; i < respBody.size(); i += MAXP) {
        size_t n = std::min(MAXP, respBody.size() - i);
        auto f = frame(0x01, sid, respBody.substr(i, n));
        client->send(hdl, f.data(), f.size(), websocketpp::frame::opcode::binary, ec);
    }
    auto fin = frame(0x02, sid, "");
    client->send(hdl, fin.data(), fin.size(), websocketpp::frame::opcode::binary, ec);
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
        WsClient client;
        client.init_asio();
        client.set_tls_init_handler([](websocketpp::connection_hdl) {
            return websocketpp::lib::make_shared<websocketpp::lib::asio::ssl::context>(
                websocketpp::lib::asio::ssl::context::tlsv12);
        });

        std::mutex mtx;
        std::map<uint32_t, StreamBuf> streams;
        std::atomic<bool> gotUrl{false};
        std::atomic<bool> closed{false};

        client.set_message_handler([&](websocketpp::connection_hdl hdl, WsMessagePtr msg) {
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
                    client.send(hdl, "{\"type\":\"pong\",\"ts\":" + std::to_string(ts) + "}",
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
                    std::thread(serve, &client, hdl, st.method, st.path, st.body, sid, local_port).detach();
                } else if (kind == 0x03) {
                    streams.erase(it);
                }
            }
        });
        client.set_open_handler([&](websocketpp::connection_hdl hdl) {
            websocketpp::lib::error_code ec;
            std::string token = getenv("ETUNNEL_TOKEN") ? getenv("ETUNNEL_TOKEN") : "";
            std::string sub = getenv("ETUNNEL_SUBDOMAIN") ? getenv("ETUNNEL_SUBDOMAIN") : "";
            std::string hello = "{\"type\":\"hello\",\"proto\":1";
            if (!sub.empty()) hello += ",\"subdomain\":\"" + json_escape(sub) + "\"";
            if (!token.empty()) hello += ",\"token\":\"" + json_escape(token) + "\"";
            if (!g_resume.empty()) hello += ",\"resume\":\"" + json_escape(g_resume) + "\"";
            hello += ",\"client\":\"etunnel-cpp/1.0\"}";
            client.send(hdl, hello, websocketpp::frame::opcode::text, ec);
        });
        // the supervisor's only view of the connection lifecycle
        client.set_fail_handler([&](websocketpp::connection_hdl) { closed = true; });
        client.set_close_handler([&](websocketpp::connection_hdl) { closed = true; });

        websocketpp::lib::error_code ec;
        auto con = client.get_connection(server, ec);
        if (ec) {
            std::cerr << "etunnel: " << ec.message() << std::endl;
            closed = true;
        } else {
            con->add_subprotocol("etunnel-v1");
            client.connect(con);
            // Drive the asio loop by hand instead of client.run(), so that a
            // signal can break out of a live connection.
            while (!g_stopped && !closed) {
                if (client.poll() == 0) {
                    std::this_thread::sleep_for(std::chrono::milliseconds(5));
                }
            }
        }
        client.stop();

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
