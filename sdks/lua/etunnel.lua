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

local function json_escape(s)
  return (s:gsub('[%c"\\]', function(c)
    local map = { ['"'] = '\\"', ['\\'] = '\\\\', ['\n'] = '\\n', ['\t'] = '\\t', ['\r'] = '\\r' }
    return map[c] or string.format("\\u%04x", c:byte())
  end))
end

local function json_field_str(s, key)
  local v = s:match('"' .. key .. '"%s*:%s*"([^"]*)"')
  return v and v:gsub('\\(.)', { n = "\n", t = "\t" }) or nil
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

local function connect(server, local_port, token, subdomain)
  local ws = Ws.connect(server)
  local hello = '{"type":"hello","proto":1'
  if subdomain then hello = hello .. ',"subdomain":"' .. json_escape(subdomain) .. '"' end
  if token then hello = hello .. ',"token":"' .. json_escape(token) .. '"' end
  hello = hello .. ',"client":"etunnel-lua/1.0"}'
  ws:send_text(hello)

  local url
  while true do
    local op, payload = ws:recv()
    if not op then error("closed before welcome") end
    if op == 0x1 then
      if payload:find('"type":"welcome"', 1, true) then
        url = json_field_str(payload, "url")
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
      ws:send_text(('{"type":"res","streamId":%d,"status":502,"headers":{"Content-Type":["text/plain"]}}'):format(sid))
      ws:send_binary(frame(0x01, sid, "etunnel: local service error: " .. tostring(status)))
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

local server = os.getenv("ETUNNEL_SERVER") or "wss://t.ggcode.uk/tunnel"
local local_port = tonumber(arg and arg[1] or os.getenv("ETUNNEL_LOCAL") or "3000")

local ok, t = pcall(connect, server, local_port,
  os.getenv("ETUNNEL_TOKEN"), os.getenv("ETUNNEL_SUBDOMAIN"))
if not ok then
  io.stderr:write("etunnel: " .. tostring(t) .. "\n")
  os.exit(1)
end
print("TUNNEL_URL: " .. t.url)
io.stdout:flush()
t.run()
