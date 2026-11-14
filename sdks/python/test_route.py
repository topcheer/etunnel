import unittest
from etunnel import _normalize_routes, _route_for

def table(strip=False):
    return _normalize_routes([
        {"prefix": "/api", "addr": "be:1", "strip": strip},
        {"prefix": "/api/v2", "addr": "be2:1"},
        {"prefix": "/", "addr": "fe:1"},
    ])

class RouteTest(unittest.TestCase):
    def test_boundaries_longest(self):
        rs = table()
        want = {"/": "fe:1", "/x": "fe:1", "/apiv2": "fe:1", "/apiary": "fe:1",
                "/api": "be:1", "/api/": "be:1", "/api/u": "be:1",
                "/api/v2": "be2:1", "/api/v2/x?q": "be2:1"}
        for p, addr in want.items():
            self.assertEqual(_route_for(rs, p)[0], addr, p)
    def test_strip(self):
        rs = _normalize_routes([{"prefix": "/api", "addr": "be:1", "strip": True},
                                {"prefix": "/", "addr": "fe:1", "strip": True}])
        self.assertEqual(_route_for(rs, "/api/u?x=1&y=2"), ("be:1", "/u?x=1&y=2"))
        self.assertEqual(_route_for(rs, "/api"), ("be:1", "/"))
        self.assertEqual(_route_for(rs, "/api/"), ("be:1", "/"))
        self.assertEqual(_route_for(rs, "/app?c=1"), ("fe:1", "/app?c=1"))
    def test_passthrough(self):
        self.assertEqual(_route_for(table(), "/api/users?x=1"), ("be:1", "/api/users?x=1"))
    def test_no_match(self):
        rs = _normalize_routes([{"prefix": "/api", "addr": "be:1"}])
        self.assertIsNone(_route_for(rs, "/other"))
        self.assertIsNotNone(_route_for(rs, "/api/x"))
    def test_validation(self):
        with self.assertRaises(ValueError): _normalize_routes([{"prefix": "/", "addr": ""}])
        with self.assertRaises(ValueError): _normalize_routes([{"addr": "a:1"}])
        with self.assertRaises(ValueError): _normalize_routes([{"prefix": "api", "addr": "a:1"}])
        with self.assertRaises(ValueError): _normalize_routes(
            [{"prefix": "/api", "addr": "a:1"}, {"prefix": "/api/", "addr": "a:2"}])

if __name__ == "__main__":
    unittest.main()
