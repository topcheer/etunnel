;; #338: proto gate tests - welcome.proto > 1 rejected loudly; missing proto
;; counts as v1 (PROTOCOL.md section 3 both-sides contract).
;; Run: clojure -M tests/proto_gate_test.clj
(load-file "etunnel.clj")

;; handle-text is private in the etunnel ns; deref its var
(def handle-text #'etunnel/handle-text)

(def ^:private fails (atom 0))
(defn- ck [name got want]
  (when (not= got want)
    (swap! fails inc)
    (println "FAIL" name ": got" got "want" want)))

(let [ctx {:state (atom nil) :streams (java.util.concurrent.ConcurrentHashMap.)}
      ws {:fake true}]
  (handle-text ws ctx {:type "welcome" :url "https://x.t.test" :proto 2})
  (ck "proto=2 rejected"
      (some? (re-find #"protocol v2" (str (:proto-error @(:state ctx)))))
      true)

  (reset! (:state ctx) nil)
  (handle-text ws ctx {:type "welcome" :url "https://x.t.test"})
  (ck "missing proto defaults to v1 (url stored)"
      (some? (:url @(:state ctx)))
      true))

(if (zero? @fails)
  (println "clojure proto gate tests: PASS")
  (do (println "clojure proto gate tests: FAIL")
      (System/exit 1)))
