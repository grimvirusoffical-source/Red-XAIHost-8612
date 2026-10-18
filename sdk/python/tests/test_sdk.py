import os
import sys
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from redx import RedX, RedXError, BoxTextError, from_box, escape_path  # noqa: E402
from tests.harness import Host  # noqa: E402

P1 = "players.red-xai.db:Player[ID=1]"
host = admin = rw = ro = scoped = None


def setUpModule():
    global host, admin, rw, ro, scoped
    host = Host()
    admin = RedX(host.url, host.admin_key)

    def mk(**kw):
        return RedX(host.url, admin.create_key(db="default", **kw)["key"])

    rw = mk(scope="write", label="sdk-rw", rate_rps=1000, rate_burst=1000)
    ro = mk(scope="read", label="sdk-ro", rate_rps=1000, rate_burst=1000)
    scoped = mk(scope="write", prefix="sess.", label="sdk-prefix")


def tearDownModule():
    if host:
        host.stop()


def wait_for(cond, timeout=5.0):
    t0 = time.time()
    while not cond():
        if time.time() - t0 > timeout:
            raise AssertionError("timed out waiting")
        time.sleep(0.02)


class DecoderTests(unittest.TestCase):
    def test_values(self):
        self.assertEqual(from_box('"a\\"b\\u00e9"'), 'a"bé')
        self.assertEqual(from_box("-1.5e2"), -150.0)
        self.assertEqual(from_box("7"), 7)
        self.assertIsInstance(from_box("7"), int)
        self.assertIsNone(from_box("NELL"))
        self.assertEqual(from_box(';["a", 2, TRUE]:'), ["a", 2, True])
        self.assertEqual(from_box("[ {A[]} = 1, {B[ID=7]} = [ {C[]} = ;[]: ] ]"), {"A": 1, "B[ID=7]": {"C": []}})
        self.assertEqual(list(from_box("[ {__proto__[]} = 1 ]")), ["__proto__"])
        self.assertEqual(from_box('"f":B[ID=1]'), {"$link": '"f":B[ID=1]'})

    def test_bad_input(self):
        for bad in ["", "[", '"x', "[ {A[]} 1 ]", "WAT", "1 2", ";[1,]:"]:
            with self.assertRaises(BoxTextError, msg=bad):
                from_box(bad)
        with self.assertRaises(BoxTextError):
            from_box(";[" * 5000)

    def test_escape(self):
        self.assertEqual(escape_path("f.red-xai.db:Box[ID=1].Name"), "f.red-xai.db%3ABox%5BID%3D1%5D.Name")
        with self.assertRaises(TypeError):
            escape_path("")


class ClientTests(unittest.TestCase):
    def test_constructor_and_key_secrecy(self):
        for args in [("", "k"), ("http://x", ""), ("ftp://x", "k"), ("http://x", "k", "../x")]:
            with self.assertRaises(TypeError):
                RedX(*args)
        import pickle
        with self.assertRaises(TypeError):
            pickle.dumps(rw)
        self.assertNotIn("rx_live_", repr(rw))
        self.assertNotIn("rx_live_", str(rw))

    def test_health_stats(self):
        self.assertEqual(RedX(host.url, "rx_live_x").health()["status"], "ok")
        self.assertIn("default", rw.stats()["dbs"])

    def test_round_trip(self):
        v = {"Name": 'ann "the" \\ ünï', "Level": 5, "Ratio": 0.25, "Tags": ["a", "b"],
             "Stats": {"Gold": 3}, "Ok": True, "Gone": None}
        self.assertTrue(rw.set(P1, v)["ok"])
        self.assertEqual(rw.get(P1), v)
        self.assertEqual(rw.get(P1 + ".Name"), v["Name"])
        self.assertEqual(rw.get(P1 + ".Stats.Gold"), 3)
        self.assertIn('{Name[]} = "ann', rw.get_box(P1))
        rw.set_box("players.red-xai.db:Player[ID=2]", '[ {Name[]} = "bob", {Level[]} = 9 ]')
        self.assertEqual(rw.get("players.red-xai.db:Player[ID=2]"), {"Level": 9, "Name": "bob"})
        rw.delete(P1)
        with self.assertRaises(RedXError) as cm:
            rw.get(P1)
        self.assertEqual(cm.exception.status, 404)
        self.assertTrue(cm.exception.not_found)
        self.assertEqual(rw.get_or(P1, "none"), "none")

    def test_query(self):
        for i in range(10, 13):
            rw.set(f"q.red-xai.db:Row[ID={i}]", {"Name": f"n{i}"})
        r = rw.query("q.red-xai.db:Row[ID=*].Name")
        self.assertEqual(r["count"], 3)
        self.assertEqual(sorted(r["values"]), ["n10", "n11", "n12"])
        with self.assertRaises(TypeError):
            rw.query("")

    def test_ttl(self):
        p = "sess.red-xai-db-qs:Q[ID=1].Otp"
        rw.set(p, "abc", ttl=1)
        self.assertEqual(rw.get(p), "abc")
        rw.set_box("sess.red-xai-db-qs:Q[ID=2].Otp", '"slide"', ttl=30, sliding=True)
        self.assertEqual(rw.get("sess.red-xai-db-qs:Q[ID=2].Otp"), "slide")
        time.sleep(2.6)
        # an expired or never-written path is always 404 (never 200 with NELL)
        self.assertEqual(rw.get_or(p, "x"), "x")
        self.assertEqual(rw.get_or("sess.red-xai-db-qs:Q[ID=77].Otp", "x"), "x")
        with self.assertRaises(RedXError) as cm:
            rw.get(p)
        self.assertEqual(cm.exception.status, 404)
        self.assertEqual(rw.get_or("sess.red-xai-db-qs:Q[ID=2].Otp", "gone"), "slide")

    def test_sql(self):
        rw.set("sqlt.red-xai.db:P[ID=1]", {"Name": "Ann", "Level": 30})
        rw.set("sqlt.red-xai.db:P[ID=2]", {"Name": "Bob", "Level": 5})
        r = rw.sql("SELECT Name FROM sqlt.P WHERE Level > ? ORDER BY Name", [10])
        self.assertEqual((r["kind"], r["count"], r["columns"]), ("select", 1, ["Name"]))
        w = rw.sql("UPDATE sqlt.P SET Level = ? WHERE _id = ?", [6, 2])
        self.assertEqual(w["affected"], 1)
        self.assertEqual(rw.get("sqlt.red-xai.db:P[ID=2].Level"), 6)
        with self.assertRaises(RedXError) as cm:
            rw.sql("SELEKT")
        self.assertEqual(cm.exception.status, 400)
        with self.assertRaises(RedXError) as cm:
            ro.sql("DELETE FROM sqlt.P WHERE _id = 1")
        self.assertEqual(cm.exception.status, 403)
        self.assertEqual(rw.query("sqlt.red-xai.db:P[ID=*].Name")["count"], 2)
        with self.assertRaises(TypeError):
            rw.sql("")

    def test_auth(self):
        bad = RedX(host.url, "rx_live_" + "A" * 43)
        for f in (lambda: bad.get(P1), bad.stats, lambda: bad.set(P1, 1)):
            with self.assertRaises(RedXError) as cm:
                f()
            self.assertEqual(cm.exception.status, 401)
            self.assertTrue(cm.exception.unauthorized)
            self.assertNotIn("A" * 20, str(cm.exception))
        with self.assertRaises(RedXError) as cm:
            ro.set(P1, {"Name": "x"})
        self.assertTrue(cm.exception.forbidden)
        with self.assertRaises(RedXError) as cm:
            ro.delete(P1)
        self.assertEqual(cm.exception.status, 403)
        with self.assertRaises(RedXError) as cm:
            rw.create_key(scope="read")
        self.assertEqual(cm.exception.status, 403)
        rw.set(P1, {"Name": "ro-can-read"})
        self.assertEqual(ro.get(P1 + ".Name"), "ro-can-read")
        scoped.set("sess.red-xai-db-qs:Z[ID=1].V", "ok", ttl=60)
        with self.assertRaises(RedXError) as cm:
            scoped.set(P1, 1)
        self.assertEqual(cm.exception.status, 403)
        with self.assertRaises(RedXError) as cm:
            scoped.query("sess.red-xai-db-qs:Z[ID=*]")
        self.assertEqual(cm.exception.status, 403)
        dead = RedX("http://127.0.0.1:1", host.admin_key, timeout=2)
        with self.assertRaises(RedXError) as cm:
            dead.stats()
        self.assertEqual(cm.exception.status, 0)
        self.assertNotIn(host.admin_key, str(cm.exception))

    def test_rate_limit(self):
        slow = RedX(host.url, admin.create_key(db="default", scope="read", rate_rps=1, rate_burst=1)["key"])
        hit = None
        for _ in range(10):
            try:
                slow.stats()
            except RedXError as e:
                hit = e
                break
        self.assertIsNotNone(hit)
        self.assertEqual(hit.status, 429)
        self.assertTrue(hit.rate_limited)
        self.assertGreaterEqual(hit.retry_after, 0)

    def test_databases_with_db(self):
        self.assertIn("default", str(admin.databases()))
        self.assertEqual(rw.with_db("default").db, "default")
        with self.assertRaises(RedXError) as cm:
            rw.with_db("other").get(P1)
        self.assertIn(cm.exception.status, (403, 404))


class SubscribeTests(unittest.TestCase):
    def test_events_and_clean_close(self):
        events, errors = [], []
        sub = ro.subscribe("subs.red-xai-db-qs:*", events.append, on_error=errors.append)
        a = "subs.red-xai-db-qs:S[ID=1].V"
        rw.set(a, "one", ttl=60)
        wait_for(lambda: any(e["op"] == "set" and e["path"] == a for e in events))
        ev = next(e for e in events if e["op"] == "set")
        self.assertEqual(ev["value"], '"one"')
        self.assertEqual(ev["data"], "one")
        self.assertTrue(ev["at"])
        rw.delete(a)
        wait_for(lambda: any(e["op"] == "delete" and e["path"] == a for e in events))
        rw.set("subs.red-xai-db-qs:S[ID=2].V", "short", ttl=1)
        wait_for(lambda: any(e["op"] == "expire" for e in events), 8)
        n = len(events)
        rw.set(P1, {"Name": "elsewhere"})
        time.sleep(0.3)
        self.assertEqual(len(events), n)
        sub.ping()
        sub.close()
        self.assertTrue(sub.wait_closed(3))
        self.assertEqual(sub.close_code, 1000)
        self.assertEqual(errors, [])

    def test_iterator_api(self):
        with ro.subscribe("subs3.red-xai-db-qs:*") as sub:
            rw.set("subs3.red-xai-db-qs:S[ID=1].V", "x", ttl=60)
            ev = sub.get(timeout=5)
            self.assertEqual((ev["op"], ev["data"]), ("set", "x"))
            threading.Timer(0.3, lambda: rw.delete("subs3.red-xai-db-qs:S[ID=1].V")).start()
            seen = []
            for e in sub:
                seen.append(e["op"])
                break
            self.assertEqual(seen, ["delete"])
        self.assertTrue(sub.wait_closed(3))
        self.assertEqual(list(sub), [])  # finished iterators stay finished

    def test_refused_and_handler_errors(self):
        bad = RedX(host.url, "rx_live_" + "B" * 43)
        with self.assertRaises(RedXError) as cm:
            bad.subscribe("x")
        self.assertEqual(cm.exception.status, 401)
        self.assertNotIn("BBBB", str(cm.exception))
        errors = []

        def boom(_ev):
            raise RuntimeError("boom")

        sub = ro.subscribe("subs2.red-xai-db-qs:*", boom, on_error=lambda e: errors.append(str(e)))
        rw.set("subs2.red-xai-db-qs:S[ID=1].V", "x", ttl=60)
        wait_for(lambda: "boom" in errors)
        sub.close()
        self.assertTrue(sub.wait_closed(3))

    def test_bare_file_subscribe_is_an_error(self):
        for bare in ("players", "players.red-xai.db"):
            with self.assertRaises(RedXError) as cm:
                rw.subscribe(bare)
            self.assertEqual(cm.exception.status, 400)

    def test_server_shutdown_surfaces_as_close(self):
        h2 = Host()
        try:
            a2 = RedX(h2.url, h2.admin_key)
            closed = []
            sub = a2.subscribe("x.red-xai.db:*", on_close=lambda c, r: closed.append(c))
        finally:
            h2.stop()
        self.assertTrue(sub.wait_closed(5))
        wait_for(lambda: closed)
        self.assertIn(closed[0], (1006, 1001))


if __name__ == "__main__":
    unittest.main()
