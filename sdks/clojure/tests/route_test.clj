;; Route table tests (same semantics table as sdks/node/route.js).
;; Run: clojure -M tests/route_test.clj
(load-file "etunnel.clj")

;; the router helpers are private in the etunnel ns; deref their vars
(def normalize-routes #'etunnel/normalize-routes)
(def route-for #'etunnel/route-for)

(def ^:private fails (atom 0))
(defn- ck [name got want]
  (when (not= got want)
    (swap! fails inc)
    (println "FAIL" name ": got" got "want" want)))
(defn- rt [prefix addr & [strip]] {:prefix prefix :addr addr :strip strip})
(defn- msg [body]
  (try (body) "accepted"
       (catch Exception e (.getMessage e))))

(let [rs (normalize-routes [(rt "/" "fe:1") (rt "/api" "be:1") (rt "/api/v2" "be2:1")])]
  (ck "/apiv2 -> /" (:addr (route-for rs "/apiv2")) "fe:1")
  (ck "/apiary -> /" (:addr (route-for rs "/apiary")) "fe:1")
  (ck "/api exact" (:addr (route-for rs "/api")) "be:1")
  (ck "/api/u" (:addr (route-for rs "/api/u")) "be:1")
  (ck "longest wins" (:addr (route-for rs "/api/v2/x?q")) "be2:1"))

(let [ss (normalize-routes [(rt "/api" "be:1" true) (rt "/" "fe:1" true)])]
  (ck "strip keeps query" (:path (route-for ss "/api/u?x=1&y=2")) "/u?x=1&y=2")
  (ck "exact strip -> /" (:path (route-for ss "/api")) "/")
  (ck "strip root prefix = original" (:path (route-for ss "/app?c=1")) "/app?c=1"))

(ck "no match -> nil" (route-for (normalize-routes [(rt "/api" "be:1")]) "/other") nil)

(ck "addr required" (.contains ^String (msg #(normalize-routes [(rt "/" "")])) "addr is required") true)
(ck "must start with /" (.contains ^String (msg #(normalize-routes [(rt "api" "a:1")])) "must start with") true)
(ck "duplicate" (.contains ^String (msg #(normalize-routes [(rt "/api" "a:1") (rt "/api/" "a:2")])) "duplicate") true)

(if (zero? @fails)
  (println "clojure route tests: PASS")
  (do (println "clojure route tests: FAIL (" @fails ")") (System/exit 1)))
