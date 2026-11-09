;; etunnel SDK for Clojure — dependencies: cheshire (JSON) via deps.edn.
;; Uses the JDK 11+ built-in WebSocket + HttpClient; buffer-based protocol
;; client (bodies buffered per stream; no WS upgrades).
;;
;; Run:   clojure -M -m etunnel 3000
;; Embed: (def t (etunnel/connect! "wss://t.etun.uk/tunnel" 3000))
;;        (:url @t)        (etunnel/close! @t)
(ns etunnel
  (:require [cheshire.core :as json])
  (:import [java.net URI]
           [java.net.http HttpClient HttpClient$Version HttpRequest
            HttpRequest$BodyPublishers HttpResponse$BodyHandlers WebSocket
            WebSocket$Listener]
           [java.nio ByteBuffer]
           [java.util.concurrent ConcurrentHashMap]))

(def ^:const maxp 32768)
(def ^:private hop
  #{"connection" "transfer-encoding" "keep-alive" "upgrade" "proxy-connection"
    "te" "trailer" "content-length" "host"})

(defn- frame
  "kind 0x01 data / 0x02 fin / 0x03 rst"
  [kind sid ^bytes payload]
  (let [b (byte-array (+ 9 (alength payload)))]
    (aset-byte b 0 kind)
    (doseq [[idx val] {1 24, 2 16, 3 8}]
      (aset-byte b idx (unchecked-byte (bit-and (bit-shift-right sid val) 0xFF))))
    (aset-byte b 4 (unchecked-byte (bit-and sid 0xFF)))
    (let [n (alength payload)]
      (doseq [[idx val] {5 24, 6 16, 7 8}]
        (aset-byte b idx (unchecked-byte (bit-and (bit-shift-right n val) 0xFF))))
      (aset-byte b 8 (unchecked-byte (bit-and n 0xFF)))
      (System/arraycopy payload 0 b 9 n))
    b))

(defn- send-frame! [^WebSocket ws kind sid payload]
  (.join (.sendBinary ws (ByteBuffer/wrap (frame kind sid payload)) true)))

(defn- send-text! [^WebSocket ws ^String s]
  (.join (.sendText ws s true)))

(defn- parse-frame
  "returns [kind sid payload-bytes]"
  [^bytes b]
  (when (>= (alength b) 9)
    (let [u (fn [i] (bit-and (aget b i) 0xFF))
          sid (bit-or (bit-shift-left (u 1) 24) (bit-shift-left (u 2) 16)
                      (bit-shift-left (u 3) 8) (u 4))
          n   (bit-or (bit-shift-left (u 5) 24) (bit-shift-left (u 6) 16)
                      (bit-shift-left (u 7) 8) (u 8))
          payload (java.util.Arrays/copyOfRange b 9 (min (+ 9 n) (alength b)))]
      [(u 0) sid payload])))

(defn- local-request
  "Executes one request against the local service. Returns [status headers body-bytes]."
  [^HttpClient http local method path ^bytes body]
  (try
    (let [builder (-> (HttpRequest/newBuilder)
                      (.uri (URI/create (str "http://" local path)))
                      (.timeout (java.time.Duration/ofSeconds 120)))
          _ (.method builder method
                     (HttpRequest$BodyPublishers/ofByteArray body))
          resp (.send ^HttpClient http (.build builder) (HttpResponse$BodyHandlers/ofByteArray))
          hdrs (->> (.map (.headers resp))
                    .entrySet
                    (mapcat (fn [^java.util.Map$Entry e]
                              (map (fn [v] [(str (.getKey e)) (str v)])
                                   (.getValue e))))
                    (remove (fn [[k _]] (contains? hop (clojure.string/lower-case k))))
                    (group-by first)
                    (map (fn [[k kvs]] [k (mapv second kvs)]))
                    (into {}))]
      [(.statusCode resp) hdrs (.body resp)])
    (catch Exception e
      [502 {"Content-Type" ["text/plain"]}
       (.getBytes (str "etunnel: local service error: " (.getMessage e) "\n"))])))

(defn- handle-text
  [ws ctx m]
  (case (:type m)
    "welcome" (reset! (:state ctx)
                {:url (:url m) :ws ws
                 ;; server-signed token: reclaim the same subdomain on reconnect
                 :resume-token (or (:resumeToken m) "")
                 :close! (fn []
                           (send-text! ws (json/generate-string
                                            {:type "bye" :reason "client closed"}))
                           (.join (.sendClose ws WebSocket/NORMAL_CLOSURE "bye")))})
    "req" (do
              (.put ^ConcurrentHashMap (:streams ctx)
                  (long (:streamId m))
                  {:req m :body (java.io.ByteArrayOutputStream.)})
              (println "DBG stored-ok"))
    "ping" (locking (:out-lock ctx)
             (send-text! ws (json/generate-string {:type "pong" :ts (or (:ts m) 0)})))
    "bye" (when-let [f (:close! @(:state ctx))] (f))
    nil))

(defn- serve-stream
  [ctx sid st]
  (try
    (let [req (:req st)
          body (.toByteArray ^java.io.ByteArrayOutputStream (:body st))
          [status hdrs out] (local-request (:http ctx) (:local ctx)
                              (:method req "GET") (:path req "/") body)
          ws ^WebSocket (:ws @(:state ctx))]
      (locking (:out-lock ctx)
        (send-text! ws (json/generate-string {:type "res" :streamId sid
                                              :status status :headers hdrs}))
        (loop [i 0]
          (when (< i (alength ^bytes out))
            (send-frame! ws 0x01 sid
                         (java.util.Arrays/copyOfRange ^bytes out i
                           (min (+ i maxp) (alength ^bytes out))))
            (recur (+ i maxp))))
        (send-frame! ws 0x02 sid (byte-array 0))))
    (catch Exception e
      (binding [*out* *err*]
        (println "serve-error:" (.getMessage e))))))

(defn- handle-binary
  [ctx ^bytes b]
  (when-let [[kind sid payload] (parse-frame b)]
    
    (when-let [st (.get ^ConcurrentHashMap (:streams ctx) sid)]
      (case kind
        1 (.write ^java.io.ByteArrayOutputStream (:body st) payload)
        2 (do (.remove ^ConcurrentHashMap (:streams ctx) sid)
              (future (serve-stream ctx sid st)))
        3 (.remove ^ConcurrentHashMap (:streams ctx) sid)
        nil))))

(defn connect!
  "Connects to the etunnel server exposing the local service at local
  (\"127.0.0.1:3000\" or a port number). Returns a deref-able state map with
  :url filled once the server assigns the hostname, plus :ws and :close!."
  [server local & {:keys [token subdomain resume]}]
  (let [local (let [s (str local)]
                (if (re-matches #"\d+" s) (str "127.0.0.1:" s) s))
        state (atom nil)
        streams (ConcurrentHashMap.)
        out-lock (Object.)
        http (.. (HttpClient/newBuilder)
                 (version HttpClient$Version/HTTP_1_1)
                 (proxy (java.net.ProxySelector/of nil))
                 (connectTimeout (java.time.Duration/ofSeconds 10))
                 build)
        listener (proxy [WebSocket$Listener] []
                   (onOpen [ws] (.request ws 1) nil)
                   (onText [ws data _]
                     (handle-text ws {:state state :streams streams :out-lock out-lock}
                                  (json/parse-string (str data) true))
                     (.request ws 1)
                     nil)
                   (onBinary [ws data _]
                     (let [b (byte-array (.remaining data))]
                       (.get data b)
                       (handle-binary {:state state :streams streams
                                       :out-lock out-lock :http http :local local} b)
                       (.request ws 1)
                       nil))
                   (onPing [ws msg]
                     (.join (.sendPong ws msg))
                     (.request ws 1)
                     nil))
        ws (.. (HttpClient/newHttpClient)
               newWebSocketBuilder
               (subprotocols "etunnel-v1" (into-array String []))
               (buildAsync (URI/create server) listener)
               join)
        _ (send-text! ws (json/generate-string
                           (cond-> {:type "hello" :proto 1 :client "etunnel-clojure/1.0"}
                             subdomain (assoc :subdomain subdomain)
                             token (assoc :token token)
                             (seq resume) (assoc :resume resume))))
        deadline (+ (System/currentTimeMillis) 15000)]
    (while (and (nil? @state) (< (System/currentTimeMillis) deadline))
      (Thread/sleep 50))
    (or @state (throw (RuntimeException. "etunnel: welcome timeout")))))

(defn- next-delay
  "Exponential backoff step, capped at 30s."
  [delay]
  (min 30000 (* delay 2)))

(defn- jitter
  "+/-20% jitter so many clients don't retry in lockstep."
  [delay]
  (long (* delay (+ 0.8 (* (rand) 0.4)))))

(defn- try-connect!
  "connect! that reports failures on stderr and returns nil instead of throwing."
  [server local token subdomain resume]
  (try
    (connect! server local :token token :subdomain subdomain :resume resume)
    (catch Exception e
      (binding [*out* *err*]
        (println "etunnel:" (.getMessage e)))
      nil)))

(defn- await-disconnect
  "Blocks while the socket is still open, or until stopped is set."
  [^WebSocket ws stopped]
  (while (and (not @stopped) (not (.isInputClosed ws)) (not (.isOutputClosed ws)))
    (Thread/sleep 200)))

(defn -main [& args]
  (let [server (or (System/getenv "ETUNNEL_SERVER") "wss://t.etun.uk/tunnel")
        local (or (first args) (System/getenv "ETUNNEL_LOCAL") "3000")
        token (System/getenv "ETUNNEL_TOKEN")
        subdomain (System/getenv "ETUNNEL_SUBDOMAIN")]
    (if (= "0" (System/getenv "ETUNNEL_RECONNECT"))
      ;; original single-shot behavior: connect once, keep alive until interrupted
      (try
        (let [t (connect! server local :token token :subdomain subdomain)]
          (println "TUNNEL_URL:" (:url t))
          (flush)
          @(promise))
        (catch Exception e
          (binding [*out* *err*]
            (println "etunnel:" (.getMessage e)))
          (System/exit 1)))
      (let [stopped (atom false)]
        (.addShutdownHook (Runtime/getRuntime)
                          (Thread. (reify Runnable
                                     (run [_] (reset! stopped true)))))
        (let [resume (atom "")] ; server-signed token: reclaim the same subdomain
          (loop [delay 500]
          (let [t (try-connect! server local token subdomain
                                (let [r @resume] (when (seq r) r)))]
            (if (nil? t)
              (when-not @stopped
                (Thread/sleep (jitter delay))
                (recur (next-delay delay)))
              (do
                (when (seq (:resume-token t)) (reset! resume (:resume-token t)))
                (println "TUNNEL_URL:" (:url t))
                (flush)
                (await-disconnect (:ws t) stopped)
                (when-not @stopped
                  (binding [*out* *err*]
                    (println "etunnel: disconnected, reconnecting"))
                  (Thread/sleep (jitter 500)) ; delay resets after a success
                  (recur 1000))))))))))
