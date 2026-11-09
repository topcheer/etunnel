;; Minimal etunnel example (Clojure).
;;   clojure -M -i etunnel.clj -i example/basic.clj
(require '[etunnel])

(def t (etunnel/connect! "wss://t.etun.uk/tunnel" 3000))
(println "public URL:" (:url @t))
@(promise) ; keep the tunnel alive
