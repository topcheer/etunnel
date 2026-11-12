-- etunnel SDK for Lua (uses lua-http's http.websocket if available, otherwise
-- falls back to a hand-rolled RFC 6455 client over LuaSocket + ssl.https —
-- the hand-rolled path needs only LuaSocket + LuaSec, both widely available).
-- Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
--
-- Run: lua etunnel.lua 3000
-- Env: ETUNNEL_SERVER, ETUNNEL_LOCAL, ETUNNEL_TOKEN, ETUNNEL_SUBDOMAIN

local MAXP = 32768

local function frame(kind, sid, payload)
  payload = payload or ""
  return string.pack(">BI4I4", kind, sid, #payload) .. payload
end

-- -------- hand-rolled websocket client (LuaSocket / LuaSec) ------------------

local Ws = {}
Ws.__index = Ws

function Ws.connect(url)
  local scheme, host, port, path = url:match("^(wss?)://([^/:]+):?(%d*)(.*)$")
  assert(scheme, "bad url " .. tostring(url))
  port = tonumber(port) or (scheme == "wss" and 443 or 80)
  path = (path and #path > 0) and path or "/"

  local sock
  if scheme == "wss" then
    local https = require("ssl.https")
    local ssl = require("ssl")
    local socket = require("socket")
    local tcp = socket.tcp()
    tcp:settimeout(15000)
    assert(tcp:connect(host, port))
    local ca = "/etc/ssl/certs/ca-certificates.crt"
    if not io.open(ca) then ca = "/etc/ssl/cert.pem" end
    sock = ssl.wrap(tcp, { mode = "client", protocol = "any", verify = "peer",
      options = "all", cafile = ca, servername = host })
    if not sock:dohandshake() then
      error("tls handshake failed")
    end
  else
    local socket = require("socket")
    sock = socket.tcp()
    sock:settimeout(15000)
    assert(sock:connect(host, port))
  end

  local rand = function(n)
    local t = {}
    for i = 1, n do t[i] = string.char(math.random(0, 255)) end
    return table.concat(t)
  end
  local key = ((require("mime")).b64(rand(16))):gsub("[\r\n]", "")
  local req = ("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
    .. "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n"
    .. "Sec-WebSocket-Protocol: etunnel-v1\r\n\r\n"):format(path, host, key)
  sock:send(req)
  local resp = ""
  while not resp:find("\r\n\r\n", 1, true) do
    local chunk = sock:receive("*l")
    if not chunk then error("handshake failed") end
    resp = resp .. chunk .. "\r\n"
  end
  assert(resp:find("HTTP/1.1 101", 1, true), "websocket rejected: " .. resp:sub(1, 40))

  return setmetatable({ sock = sock }, Ws)
end

function Ws:send_frame(opcode, payload)
  payload = payload or ""
  local mask = ""
  for _ = 1, 4 do mask = mask .. string.char(math.random(0, 255)) end
  local masked = {}
  for i = 1, #payload do
    masked[i] = string.char(payload:byte(i) ~ mask:byte(((i - 1) % 4) + 1))
  end
  masked = table.concat(masked)
  local n = #payload
  local hdr = string.char(0x80 | opcode)
  if n < 126 then
    hdr = hdr .. string.char(0x80 | n)
  elseif n < 65536 then
    hdr = hdr .. string.char(0x80 | 126) .. string.pack(">I2", n)
  else
    hdr = hdr .. string.char(0x80 | 127) .. string.pack(">I8", n)
  end
  self.sock:send(hdr .. mask .. masked)
end

function Ws:send_text(s) self:send_frame(0x1, s) end
function Ws:send_binary(s) self:send_frame(0x2, s) end

local function read_exact(sock, n)
  local got = ""
  while #got < n do
    local chunk, err = sock:receive(n - #got)
    if not chunk then error(err or "eof") end
    got = got .. chunk
  end
  return got
end

-- returns opcode, payload ; nil on close
function Ws:recv()
  while true do
    local h = read_exact(self.sock, 2)
    local opcode = h:byte(1) & 0x0F
    local masked = (h:byte(2) & 0x80) ~= 0
    local len = h:byte(2) & 0x7F
    if len == 126 then
      len = string.unpack(">I2", read_exact(self.sock, 2))
    elseif len == 127 then
      len = string.unpack(">I8", read_exact(self.sock, 8))
    end
    local mask = masked and read_exact(self.sock, 4) or nil
    local payload = len > 0 and read_exact(self.sock, len) or ""
    if masked then
      local out = {}
      for i = 1, #payload do
        out[i] = string.char(payload:byte(i) ~ mask:byte(((i - 1) % 4) + 1))
      end
      payload = table.concat(out)
    end
    if opcode == 0x8 then return nil, nil end
    if opcode == 0x9 then
      self:send_frame(0xA, payload)
    elseif opcode ~= 0xA then
      return opcode, payload
    end
  end
end

-- -------- minimal JSON -------------------------------------------------------

-- Decode JSON string escapes in a single left-to-right pass: \" \\ \/ \b \f
-- \n \r \t and \uXXXX (encoded as UTF-8; lone surrogates fold to U+FFFD).
-- Chained gsub passes would mis-read sequences like "\\u0041" (an escaped
-- backslash followed by a literal u0041), so walk the string once instead.
local function json_unescape(s)
  local map = { ['"'] = '"', ['\\'] = '\\', ['/'] = '/', b = "\b", f = "\f", n = "\n", r = "\r", t = "\t" }
  local out, j = {}, 1
  while true do
    local k = s:find("\\", j, true)
    if not k then break end
    out[#out + 1] = s:sub(j, k - 1)
    local c = s:sub(k + 1, k + 1)
    if c == "u" then
      local cp = tonumber(s:sub(k + 2, k + 5), 16)
      if cp then
        if cp >= 0xD800 and cp <= 0xDFFF then cp = 0xFFFD end
        if cp < 0x80 then
          out[#out + 1] = string.char(cp)
        elseif cp < 0x800 then
          out[#out + 1] = string.char(192 + math.floor(cp / 64), 128 + cp % 64)
        else
          out[#out + 1] = string.char(224 + math.floor(cp / 4096), 128 + math.floor(cp / 64) % 64, 128 + cp % 64)
        end
        j = k + 6
      else
        out[#out + 1] = c
        j = k + 2
      end
    else
      out[#out + 1] = map[c] or c
      j = k + 2
    end
  end
  out[#out + 1] = s:sub(j)
  return table.concat(out)
end

local function json_escape(s)
  return (s:gsub('[%c"\\]', function(c)
    local map = { ['"'] = '\\"', ['\\'] = '\\\\', ['\n'] = '\\n', ['\t'] = '\\t', ['\r'] = '\\r' }
    return map[c] or string.format("\\u%04x", c:byte())
  end))
end

-- Extract a string field, tolerating escaped quotes inside the value: a plain
-- "([^"]*)" match would stop at the quote of a\"b and truncate it.
local function json_field_str(s, key)
  local open = s:match('"' .. key .. '"%s*:%s*"()')
  if not open then return nil end
  local i = open
  while true do
    local c = s:find('["\\]', i)
    if not c then return nil end -- malformed: unterminated string
    if s:sub(c, c) == '"' then
      return json_unescape(s:sub(open, c - 1))
    end
    i = c + 2 -- skip the escaped character
  end
end

local function json_field_num(s, key)
  return tonumber(s:match('"' .. key .. '"%s*:%s*([%-%d%.eE+]+)'))
end

-- -------- local HTTP over raw LuaSocket --------------------------------------

local function local_request(host, port, method, path, body)
  local socket = require("socket")
  local sock = socket.tcp()
  sock:settimeout(30000)
  assert(sock:connect(host, port))
  local req = ("%s %s HTTP/1.1\r\nHost: %s:%d\r\nContent-Length: %d\r\nConnection: close\r\n\r\n")
    :format(method, path, host, port, body and #body or 0)
  sock:send(req .. (body or ""))
  local resp = ""
  repeat
    local chunk = sock:receive("*a")
    resp = resp .. (chunk or "")
  until not chunk or #chunk == 0
  sock:close()
  local status = tonumber(resp:match("^HTTP/[%d%.]+ (%d+)")) or 502
  local head, rest = resp:match("^(.-\r\n\r\n)(.*)$")
  local cl = tonumber(head and head:match("[Cc]ontent%-Length:%s*(%d+)"))
  local out = rest or ""
  if cl then out = out:sub(1, cl) end
  return status, head or "", out
end

-- -------- etunnel -------------------------------------------------------------

-- Themed 502 page shown when the local app is down (same HTML as the other SDKs).
local ERR_PAGE_A = [[
<!doctype html><html><head><meta charset="utf-8">
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
<div class="k">error</div><div class="v err">]]

local ERR_PAGE_B = [[</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>]]

local function local_error_page(msg)
  local esc = tostring(msg):gsub("&", "&amp;")
  esc = esc:gsub("<", "&lt;")
  esc = esc:gsub(">", "&gt;")
  return ERR_PAGE_A .. esc .. ERR_PAGE_B
end

local function connect(server, local_port, token, subdomain, resume)
  local ws = Ws.connect(server)
  local hello = '{"type":"hello","proto":1'
  if subdomain then hello = hello .. ',"subdomain":"' .. json_escape(subdomain) .. '"' end
  if token then hello = hello .. ',"token":"' .. json_escape(token) .. '"' end
  if resume and resume ~= "" then hello = hello .. ',"resume":"' .. json_escape(resume) .. '"' end
  hello = hello .. ',"client":"etunnel-lua/1.4.0"}'
  ws:send_text(hello)

  local url, resume_token
  while true do
    local op, payload = ws:recv()
    if not op then error("closed before welcome") end
    if op == 0x1 then
      if payload:find('"type":"welcome"', 1, true) then
        url = json_field_str(payload, "url")
        resume_token = json_field_str(payload, "resumeToken")
        break
      elseif payload:find('"type":"error"', 1, true) then
        error("etunnel: " .. (json_field_str(payload, "code") or "?"))
      end
    end
  end

  local streams = {}
  local function serve(st, sid)
    local ok, status, head, out = pcall(local_request, "127.0.0.1", local_port,
      st.method or "GET", st.path or "/", st.body)
    if not ok then
      ws:send_text(('{"type":"res","streamId":%d,"status":502,"headers":{"Content-Type":["text/html; charset=utf-8"]}}'):format(sid))
      ws:send_binary(frame(0x01, sid, local_error_page(tostring(status))))
      ws:send_binary(frame(0x02, sid))
      return
    end
    -- response headers (re-serialize the raw header block)
    local hdrs = {}
    for line in (head:gmatch("[^\r\n]+")) do
      local k, v = line:match("^([^:]+):%s*(.*)$")
      if k and v and not k:lower():match("^content%-length$") and not k:lower():match("^connection$") then
        hdrs[#hdrs + 1] = ('"%s":["%s"]'):format(json_escape(k), json_escape(v))
      end
    end
    ws:send_text(('{"type":"res","streamId":%d,"status":%d,"headers":{%s}}')
      :format(sid, status, table.concat(hdrs, ",")))
    for i = 1, #out, MAXP do
      ws:send_binary(frame(0x01, sid, out:sub(i, i + MAXP - 1)))
    end
    ws:send_binary(frame(0x02, sid))
  end

  -- single-threaded loop: Lua coroutines would be needed for concurrency; this
  -- serves requests sequentially (fine for webhooks/dev use).
  return {
    url = url,
    resumeToken = resume_token or "",
    run = function()
      while true do
        local op, payload = ws:recv()
        if not op then return end
        if op == 0x1 then
          if payload:find('"type":"req"', 1, true) then
            local sid = json_field_num(payload, "streamId")
            local method = json_field_str(payload, "method") or "GET"
            local path = json_field_str(payload, "path") or "/"
            streams[sid] = { method = method, path = path, body = "" }
          elseif payload:find('"type":"ping"', 1, true) then
            local ts = json_field_num(payload, "ts") or 0
            ws:send_text(('{"type":"pong","ts":%d}'):format(ts))
          elseif payload:find('"type":"bye"', 1, true) then
            return
          end
        elseif op == 0x2 and #payload >= 9 then
          local kind = payload:byte(1)
          local sid = string.unpack(">I4", payload, 2)
          local n = string.unpack(">I4", payload, 6)
          local st = streams[sid]
          if st then
            if kind == 0x01 then
              st.body = st.body .. payload:sub(10, 9 + n)
            elseif kind == 0x02 then
              streams[sid] = nil
              serve(st, sid)
            elseif kind == 0x03 then
              streams[sid] = nil
            end
          end
        end
      end
    end,
    close = function()
      ws:send_text('{"type":"bye","reason":"client closed"}')
      ws.sock:close()
    end,
  }
end

-- -------- main ----------------------------------------------------------------

local server = os.getenv("ETUNNEL_SERVER") or "wss://t.etun.uk/tunnel"
local local_port = tonumber(arg and arg[1] or os.getenv("ETUNNEL_LOCAL") or "3000")
local token, subdomain = os.getenv("ETUNNEL_TOKEN"), os.getenv("ETUNNEL_SUBDOMAIN")

-- supervisor: connect, wait for the disconnect, back off, retry. connect()
-- already prints nothing about the URL, so the URL is announced here on every
-- (re)connect. ETUNNEL_RECONNECT=0 restores the old single-attempt behaviour.
--
-- Lua has no built-in signal API: SIGINT/SIGTERM keep their default
-- disposition, which terminates the process immediately -- i.e. exactly the
-- required "stop, never reconnect" semantics, without adding a dependency.
local reconnect = os.getenv("ETUNNEL_RECONNECT") ~= "0"
local socket = require("socket")
local backoff_ms = 500
local resume = "" -- server-signed token: reclaim the same subdomain on reconnect

while true do
  local ok, t = pcall(connect, server, local_port, token, subdomain, resume)
  if ok then
    backoff_ms = 500 -- a successful connect resets the backoff
    if t.resumeToken and t.resumeToken ~= "" then resume = t.resumeToken end
    print("TUNNEL_URL: " .. tostring(t.url))
    io.stdout:flush()
    local rok, rerr = pcall(t.run) -- blocks until the tunnel drops
    if not rok then io.stderr:write("etunnel: " .. tostring(rerr) .. "\n") end
    pcall(t.close) -- best effort: the socket may already be gone
    if not rok then
      io.stderr:write("etunnel: disconnected: " .. tostring(rerr) .. "\n")
    else
      io.stderr:write("etunnel: disconnected\n")
    end
  else
    io.stderr:write("etunnel: " .. tostring(t) .. "\n")
  end

  if not reconnect then
    if not ok then os.exit(1) end
    break -- original behaviour: the script ends when the tunnel drops
  end

  local wait_ms = math.floor(backoff_ms * (0.8 + 0.4 * math.random()))
  io.stderr:write(("etunnel: reconnecting in %dms\n"):format(wait_ms))
  local slept = 0
  while slept < wait_ms do -- sliced sleep, mirrors the other SDKs
    local slice = math.min(100, wait_ms - slept)
    socket.sleep(slice / 1000)
    slept = slept + slice
  end
  backoff_ms = math.min(backoff_ms * 2, 30000)
end

-- --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
--   local creds = etunnel_login("https://t.etun.uk")   -- opens the browser
--   -- -> { api_token=..., subdomains={...}, plan=..., expires_at=0 }
-- Binds a random 127.0.0.1 port, waits for the one-time code on /callback
-- (10-minute budget), exchanges it. Persist the result and pass api_token
-- as the token option of future etunnel_connect calls. Uses LuaSocket plus
-- the https helper already present in this file.
function etunnel_login(server, open_browser, timeout)
  open_browser = open_browser or function(u)
    local cmd = (jit and jit.os == "OSX") and "open" or "xdg-open"
    os.execute(cmd .. ' "' .. u:gsub('"', '') .. '"')
    return true
  end
  timeout = timeout or 600
  server = (server or ""):gsub("/+$", "")
  assert(#server > 0, "etunnel: login: server required")

  local socket = require("socket")
  local srv = assert(socket.bind("127.0.0.1", 0))
  srv:settimeout(timeout)
  local port = srv:getsockname():match(":(%d+)$")

  local start = et_login_post(server, "/auth/cli/start",
    { redirect_uri = "http://127.0.0.1:" .. port .. "/callback" })
  assert(start.auth_url and start.device_code, "etunnel: login: empty start payload")

  local opened = false
  if open_browser then
    local ok = pcall(function() opened = open_browser(start.auth_url) end)
  end
  if not opened then
    io.stderr:write("etunnel: authorize in your browser: " .. start.auth_url .. "\n")
  end

  local client = assert(srv:accept())
  client:settimeout(10)
  local req = client:receive("*l")
  local path = req and req:match("GET%s+(%S+)") or ""
  local code = path:match("[?&]code=([^&%s]+)") or ""
  code = code:gsub("%%(%x%x)", function(h) return string.char(tonumber(h, 16)) end)
  local body = "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>"
  client:send("HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: "
    .. #body .. "\r\nConnection: close\r\n\r\n" .. body)
  client:close()
  srv:close()
  assert(#code > 0, "etunnel: login: callback without code")

  local creds = et_login_post(server, "/auth/cli/exchange",
    { device_code = start.device_code, code = code })
  assert(creds.api_token, "etunnel: login: no api_token in exchange response")
  return creds
end

-- Minimal JSON POST over the same ssl.https/plain-socket split the SDK uses.
function et_login_post(base, path, tbl)
  local ltn12 = require("ltn12")
  local https = require("ssl.https")
  local body = et_json_encode(tbl)
  local out = {}
  local url = base .. path
  local ok, code = https.request {
    url = url, method = "POST",
    headers = { ["Content-Type"] = "application/json", ["Content-Length"] = tostring(#body) },
    source = ltn12.source.string(body),
    sink = ltn12.sink.table(out),
  }
  assert(ok and tonumber(code) == 200, "etunnel: login: " .. path .. " failed: " .. tostring(code))
  local json = require("cjson.safe") or require("cjson")
  local decoded = (json.decode or json)(table.concat(out))
  assert(type(decoded) == "table", "etunnel: login: bad response from " .. path)
  return decoded
end

-- Tiny encoder for the two flat request shapes we send (no nesting needed).
function et_json_encode(tbl)
  local parts = {}
  for k, v in pairs(tbl) do
    parts[#parts+1] = '"' .. tostring(k) .. '":"' .. tostring(v):gsub('[%c"\\]', function(c)
      return string.format("\\u%04x", string.byte(c)) end) .. '"'
  end
  return "{" .. table.concat(parts, ",") .. "}"
end
