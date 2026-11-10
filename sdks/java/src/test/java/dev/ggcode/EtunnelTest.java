package dev.ggcode;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

/**
 * Issue #51 acceptance tests, dependency-free (plain main + asserts):
 *  1) header-value escaping round-trips through the SDK's own JSON reader -
 *     the old quote-then-backslash order produced invalid JSON for any header
 *     value containing a quote or a backslash;
 *  2) the welcome wait times out instead of hanging forever, and returns the
 *     value on the happy path.
 *
 * Run: javac -d /tmp/et-test src/main/java/dev/ggcode/Etunnel.java \
 *          src/test/java/dev/ggcode/EtunnelTest.java
 *      java -cp /tmp/et-test dev.ggcode.EtunnelTest
 */
public class EtunnelTest {
    static int failures = 0;

    public static void main(String[] args) throws Exception {
        String[] quotes = {"a\"b", "back\\slash", "mix \\\" q", "plain"};
        for (String v : quotes) {
            // exact composition of the serve() headers path
            String json = "{\"X\":[\"" + Etunnel.hdrVal(v) + "\"]}";
            Map<String, Object> m = Etunnel.Json.read(json);
            @SuppressWarnings("unchecked")
            List<Object> vals = (List<Object>) m.get("X");
            check(v.equals(vals.get(0)), "hdrVal round-trip: " + v);
        }

        // the pre-fix order (quotes first, then backslashes) is demonstrably
        // different - and invalid - for any value with a quote
        String buggy = "a\"b".replace("\"", "\\\"").replace("\\", "\\\\");
        check(!buggy.equals(Etunnel.hdrVal("a\"b")),
            "pre-fix order must differ (buggy=" + buggy + ", fixed=" + Etunnel.hdrVal("a\"b") + ")");
        check(buggy.contains("\\\\\"") || buggy.equals("a\\\\\"b"),
            "buggy output is the invalid a\\\\\"b form, got " + buggy);
        // control: feed the buggy composition to the same reader - it must
        // either fail to parse or corrupt the value (never round-trip)
        try {
            Map<String, Object> bm = Etunnel.Json.read("{\"X\":[\"" + buggy + "\"]}");
            @SuppressWarnings("unchecked")
            List<Object> bv = (List<Object>) bm.get("X");
            check(!"a\"b".equals(bv.get(0)), "buggy order corrupts the value (parsed: " + bv.get(0) + ")");
        } catch (Exception e) {
            check(true, "buggy order produces unparseable JSON (" + e.getClass().getSimpleName() + ")");
        }

        // welcome timeout: never-counted-down latch throws after the timeout
        Etunnel.WelcomeLatch late = new Etunnel.WelcomeLatch();
        long t0 = System.nanoTime();
        try {
            late.await(200, TimeUnit.MILLISECONDS, null);
            check(false, "untimed welcome must throw");
        } catch (RuntimeException e) {
            check(e.getMessage().contains("no welcome"), "timeout message: " + e.getMessage());
            long ms = (System.nanoTime() - t0) / 1_000_000;
            check(ms >= 190, "waited the timeout, got " + ms + "ms");
        }

        // happy path: counted-down latch returns the value immediately
        Etunnel.WelcomeLatch ok = new Etunnel.WelcomeLatch();
        Map<String, Object> val = new HashMap<>();
        ok.value = val;
        ok.latch.countDown();
        check(ok.await(1, TimeUnit.SECONDS, null) == val, "welcome value passthrough");

        System.out.println(failures == 0 ? "ALL PASS" : failures + " FAILURES");
        if (failures > 0) System.exit(1);
    }

    static void check(boolean cond, String what) {
        if (cond) System.out.println("ok: " + what);
        else { System.out.println("FAIL: " + what); failures++; }
    }
}
