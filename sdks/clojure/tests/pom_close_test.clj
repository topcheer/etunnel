;; Docs-audit regressions for the Clojure SDK packaging + public API.
;; Run: clojure -M tests/pom_close_test.clj   (from sdks/clojure)
;;
;; 1. The published POM (CI: mvn deploy:deploy-file -DpomFile=pom.xml) must
;;    carry the runtime dependency. Before the fix a consumer's
;;    {:deps {dev.ggcode/etunnel-clj {:mvn/version ...}}} died at
;;    (require 'etunnel) with "Could not locate cheshire/core__init.class".
;;    cheshire is a Clojars-only artifact, so Maven consumers also need the
;;    repository declared in the POM.
;; 2. No debug println left in the request path ("DBG stored-ok" was printed
;;    for every visitor request).
;; 3. close! is public - the header comment documented etunnel/close!, but
;;    the var did not exist (only the private :close! in the state map did).
(load-file "etunnel.clj")
(require '[clojure.string :as str])

(def failures (atom 0))
(defmacro check [label expr]
  `(do (when-not ~expr
         (swap! failures inc)
         (println "FAIL:" ~label))
       ~expr))

(def pom (slurp "pom.xml"))
(def deps-edn (slurp "deps.edn"))
(def src (slurp "etunnel.clj"))

;; --- 1. POM dependency block mirrors deps.edn ----------------------------
(check "pom declares a <dependencies> block" (.contains pom "<dependencies>"))
(check "pom depends on cheshire"
  (re-find #"(?s)<dependencies>.*?<artifactId>cheshire</artifactId>.*?</dependencies>" pom))
(check "pom declares the Clojars repository (cheshire is not on Maven Central)"
  (.contains pom "https://repo.clojars.org/"))
(let [pom-v (re-find #"(?s)<artifactId>cheshire</artifactId>\s*<version>([^<]+)</version>" pom)
      edn-v (re-find #"cheshire/cheshire\s*\{:mvn/version\s*\"([^\"]+)\"" deps-edn)]
  (check "cheshire version present in both pom.xml and deps.edn" (and pom-v edn-v))
  (check "cheshire version agrees between pom.xml and deps.edn" (= (second pom-v) (second edn-v))))
;; bump-release.py and CI read the FIRST <version> element: it must stay the
;; project's own version, not the cheshire dependency's.
(check "first <version> in pom.xml is the project version"
  (re-find #"(?s)<artifactId>etunnel-clj</artifactId>\s*<version>[^<]+</version>" pom))

;; --- 2. no debug output in the request path --------------------------------
(check "no DBG println in etunnel.clj" (not (.contains src "\"DBG ")))
;; the "req" clause must be the bare .put - no println inside the form
(let [req-line (some #(when (re-find #"^\s*\"req\"" %) %) (str/split-lines src))]
  (check "req handler exists" req-line)
  (check "req handler is a bare .put with no println"
    (and (.contains req-line ".put") (not (.contains req-line "println")))))

;; --- 3. public close! ---------------------------------------------------------
(check "etunnel/close! is a public var" (contains? (ns-publics 'etunnel) 'close!))
(check "close! without a :close! entry is a no-op returning nil"
  (nil? (etunnel/close! {:close! nil})))
(let [called (atom 0)]
  (etunnel/close! {:close! (fn [] (swap! called inc))})
  (check "close! invokes the state's :close! exactly once" (= 1 @called)))

(if (pos? @failures)
  (do (println "clojure pom/close tests:" @failures "FAILED") (System/exit 1))
  (println "clojure pom/close tests: PASS"))
(System/exit 0)
