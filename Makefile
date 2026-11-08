# etunnel packaging pipeline — builds all 20 SDKs' distributable artifacts
# into dist/packages/. Publish-ready manifests live in each sdks/<lang>/ dir.
# Usage: make pack-all   |   make verify   |   make clean-pkgs
# Languages whose toolchain is absent locally (php, lua/luarocks, elixir)
# are skipped here and built by CI (.github/workflows/sdk-packages.yml).

PACK    := dist/packages
Sdks    := sdks
V       := 1.0.0
NODE_V  := $(shell node -p "require('./$(Sdks)/node/package.json').version" 2>/dev/null || echo 1.0.0)
RUST_V  := $(shell grep -m1 '^version' $(Sdks)/rust/Cargo.toml | sed 's/.*"\(.*\)".*/\1/')
RUBY_V  := $(shell ruby -e "print Gem::Specification.load('$(Sdks)/ruby/etunnel.gemspec').version" 2>/dev/null || echo 1.0.0)

.PHONY: all verify pack-all clean-pkgs $(PACK) prereq

all: pack-all

prereq:
	@mkdir -p $(PACK)

# ---------- verification (compile-only, no artifacts) ----------
check-versions:
	python3 scripts/bump-release.py --check

verify: check-versions
	go build ./... && go test -count=1 ./... > /dev/null && echo "go: ok"
	cd $(Sdks)/node && node --check etunnel.js && echo "node: ok"
	cd $(Sdks)/python && python3 -m py_compile etunnel.py && echo "python: ok"
	cd $(Sdks)/java && javac -d /tmp/et-verify-java src/main/java/dev/ggcode/Etunnel.java && echo "java: ok"
	cd $(Sdks)/swift && swift build 2>&1 | tail -1
	cd $(Sdks)/clojure && clojure -M -e '(load-file "etunnel.clj")' > /dev/null && echo "clojure: ok"
	cd $(Sdks)/ruby && ruby -c lib/etunnel.rb > /dev/null && echo "ruby: ok"
	cd $(Sdks)/perl && perl -c etunnel.pl > /dev/null && echo "perl: ok"

# ---------- per-language pack targets ----------
pack-node: prereq
	cd $(Sdks)/node && npm pack --silent --pack-destination ../../$(PACK) \
	  && mv ../../$(PACK)/easytunnel-et-sdk-$(NODE_V).tgz ../../$(PACK)/et-sdk-node-$(NODE_V).tgz
pack-typescript: prereq
	cd $(Sdks)/typescript && npm pack --silent --pack-destination ../../$(PACK)
pack-deno: prereq
	cd $(Sdks)/deno && tar czf ../../$(PACK)/etunnel-deno-$(V).tar.gz etunnel.ts deno.json README.md
pack-python: prereq
	cd $(Sdks)/python && python3 -m pip wheel . --no-deps -w ../../$(PACK) --quiet
pack-java: prereq
	cd $(Sdks)/java && mkdir -p build && javac -d build src/main/java/dev/ggcode/Etunnel.java \
	  && jar cfe ../../$(PACK)/etunnel-java-$(V).jar dev.ggcode.Etunnel -C build . \
	  && cp pom.xml ../../$(PACK)/etunnel-java-$(V).pom
pack-kotlin: prereq
	cd $(Sdks)/kotlin && (mkdir -p build && kotlinc Etunnel.kt -include-runtime -d ../../$(PACK)/etunnel-kotlin-$(V).jar \
	  && echo "kotlin: local build ok" || echo "kotlin: build skipped (no kotlinc; source tarball only)") \
	  ; tar czf ../../$(PACK)/etunnel-kotlin-src-$(V).tar.gz Etunnel.kt build.gradle.kts README.md
pack-scala: prereq
	cd $(Sdks)/scala && (mkdir -p build && scalac -d build Etunnel.scala \
	  && jar cfe ../../$(PACK)/etunnel-scala-$(V).jar Etunnel -C build . \
	  && echo "scala: local build ok" || echo "scala: build skipped (no scalac; source tarball only)") \
	  ; tar czf ../../$(PACK)/etunnel-scala-src-$(V).tar.gz Etunnel.scala build.sbt README.md
pack-csharp: prereq
	cd $(Sdks)/csharp && dotnet pack -c Release -o ../../$(PACK) /clp:ErrorsOnly
pack-rust: prereq
	cd $(Sdks)/rust && cargo package --no-verify --allow-dirty --quiet \
	  && cp target/package/etunnel-$(RUST_V).crate ../../$(PACK)/
pack-swift: prereq
	cd $(Sdks)/swift && swift build -c release 2>&1 | tail -1 \
	  && tar czf ../../$(PACK)/etunnel-swift-$(V).tar.gz etunnel.swift Package.swift README.md
pack-dart: prereq
	cd $(Sdks)/dart && tar czf ../../$(PACK)/etunnel-dart-$(V).tar.gz etunnel.dart pubspec.yaml README.md
pack-ruby: prereq
	cd $(Sdks)/ruby && gem build etunnel.gemspec --silent \
	  && mv etunnel-$(RUBY_V).gem ../../$(PACK)/
pack-php: prereq
	cd $(Sdks)/php && tar czf ../../$(PACK)/etunnel-php-$(V).tar.gz etunnel.php README.md
pack-perl: prereq
	cd $(Sdks)/perl && (perl Makefile.PL > /dev/null 2>&1; make dist > /dev/null 2>&1) \
	  && mv etunnel-$(V).tar.gz ../../$(PACK)/etunnel-perl-$(V).tar.gz && rm -f Makefile Makefile.old pm_to_blib 2>/dev/null; true
pack-clojure: prereq
	cd $(Sdks)/clojure && (jar cf ../../$(PACK)/etunnel-clojure-$(V).jar etunnel.clj \
	  && cp pom.xml ../../$(PACK)/etunnel-clojure-$(V).pom \
	  && cp deps.edn ../../$(PACK)/etunnel-clojure-$(V).deps.edn \
	  && echo "clojure: local build ok" || echo "clojure: build skipped (no jar tool; source tarball only)") \
	  ; tar czf ../../$(PACK)/etunnel-clojure-src-$(V).tar.gz etunnel.clj deps.edn pom.xml README.md
pack-c: prereq
	cd $(Sdks)/c && (make -s etunnel > /dev/null 2>&1 && rm -f etunnel \
	  && echo "c: local build ok" || echo "c: build skipped (need libwebsockets/libcurl/json-c via pkg-config)") \
	  ; tar czf ../../$(PACK)/etunnel-c-$(V).tar.gz etunnel.c Makefile README.md
pack-cpp: prereq
	cd $(Sdks)/cpp && (cmake -B build -DCMAKE_BUILD_TYPE=Release > /dev/null 2>&1 \
	  && cmake --build build --target etunnel > /dev/null 2>&1 && rm -rf build \
	  && echo "cpp: local build ok" || echo "cpp: build skipped (websocketpp needs Boost<=1.86 or asio<=1.14; CI builds on ubuntu-22.04)") \
	  ; tar czf ../../$(PACK)/etunnel-cpp-$(V).tar.gz etunnel.cpp CMakeLists.txt README.md

# php/lua/elixir: no local toolchain — CI matrix builds them
pack-local-only = pack-node pack-typescript pack-deno pack-python pack-java \
  pack-kotlin pack-scala pack-csharp pack-rust pack-swift pack-dart pack-ruby \
  pack-php pack-perl pack-clojure pack-c pack-cpp

pack-all: $(pack-local-only)
	@echo ""; echo "=== $(PACK) ==="; ls -lh $(PACK) | awk 'NR>1 {printf "%-42s %s\n", $$9, $$5}'; \
	echo ""; echo "skipped locally (CI builds): php(composer) lua(luarocks) elixir(mix)"

clean-pkgs:
	rm -rf $(PACK)
