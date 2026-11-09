# Minimal etunnel example: serve a local HTTP page through a public tunnel.
#   python example/basic.py            # local port 3000
#   python example/basic.py 8080       # custom local port
import sys, threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, __file__.rsplit('/', 2)[0])
from etunnel import connect

port = int(sys.argv[1]) if len(sys.argv) > 1 else 3000


class Hello(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"hello from etunnel-example\n"
        self.send_response(200)
        self.send_header("content-type", "text/plain")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


httpd = HTTPServer(("127.0.0.1", port), Hello)
threading.Thread(target=httpd.serve_forever, daemon=True).start()
print(f"local http://127.0.0.1:{port}")

t = connect("wss://t.etun.uk/tunnel", port)
print("public URL:", t["url"])
threading.Event().wait()  # keep the tunnel alive
