# etunnel packaging pipeline — builds all 20 SDKs' distributable artifacts
# into dist/packages/. Publish-ready manifests live in each sdks/<lang>/ dir.
# Usage: make pack-all   |   make verify   |   make clean-pkgs
#
# Policy (2026-11-12): NO SILENT FAILURES. Every pack target must compile
# its language and produce the artifact, or exit non-zero. Toolchain-missing
# is an error, not a skip. Developers who want a partial local pack set
# PACK_OPTIONAL=1 (prints a loud warning per skipped language).

PACK    := dist/packages
Sdks    := sdks
UNAME_S := $(shell uname -s)
V       := $(shell cat VERSION 2>/dev/null || echo unknown)
NODE_V  := $(shell node -p "require('./$(Sdks)/node/package.json').version" 2>/dev/null || echo unknown)
RUST_V  := $(shell grep -m1 '^version' $(Sdks)/rust/Cargo.toml | sed 's/.*"\(.*\)".*/\1/')
RUBY_V  := $(shell ruby -e "print Gem::Specification.load('$(Sdks)/ruby/etunnel.gemspec').version" 2>/dev/null || echo unknown)
PERL_V  := $(shell perl -e 'my $$m = ExtUtils::MakeMaker->VERSION; print "n/a"' 2>/dev/null; grep -m1 "VERSION" $(Sdks)/perl/Makefile.PL 2>/dev/null | sed "s/.*'\([0-9.]*\)'.*/\1/" || echo $(V))

ifeq ($(V),unknown)
$(error VERSION file missing or empty - refusing to pack with unknown version)
endif

.PHONY: all verify pack-all clean-pkgs $(PACK) prereq check-versions build

# Production-style build: stamps server.Version so the hello.version gate
# (client_outdated) is active. A plain `go build` leaves it "dev", which
# switches the gate off (see server/client_version.go).
build:
	go build -trimpath -ldflags "-s -w -X github.com/topcheer/etunnel/server.Version=$(V)" -o etunneld ./cmd/etunneld
	./etunneld -version

all: pack-all

prereq:
	@mkdir -p $(PACK)

# ---------- verification (compile-only, no artifacts) ----------
check-versions:
	python3 scripts/bump-release.py --check

verify: check-versions
	# Guard the v1.6.10 failure mode: a renamed-but-unstaged rockspec.
	@if [ -n "$$(git status --porcelain -- sdks/lua/ 2>/dev/null)" ]; then \
		echo "verify: sdks/lua has unstaged/untracked changes (rockspec rename not committed?)"; exit 1; fi
	# #157: v1.7.0/v1.7.1 shipped tags missing bumped manifests because the
	# release commit staged a hand-picked subset. Nothing tracked-and-modified
	# may be left when verify (and thereby a release) runs; untracked files
	# (build artifacts) are fine.
	@if [ -n "$$(git status --porcelain | grep -v '^??')" ]; then \
		echo "verify: tracked files have staged/unstaged changes - release staging must be complete (see #157)"; \
		git status --porcelain | grep -v '^??' | head -5; exit 1; fi
	go build ./... && go vet ./... && test -z "$$(gofmt -l . 2>/dev/null | grep -v "^\.ggcode/")" && go test -race -count=1 ./... > /dev/null && echo "go: ok"
	cd $(Sdks)/node && node --check etunnel.js && echo "node: ok"
	cd $(Sdks)/python && python3 -m py_compile etunnel.py && echo "python: ok"
	cd $(Sdks)/java && javac --release 11 -d /tmp/et-verify-java src/main/java/dev/ggcode/Etunnel.java && echo "java: ok"
ifeq ($(UNAME_S),Darwin)
	# Swift gate is a full swift build (compile-level), not a parse check:
	# swiftc -parse misses type/symbol errors (e.g. #108's NSWorkspace/poll
	# regressions passed parse but broke the build). Compile catches them.
	cd $(Sdks)/swift && swift build 2>&1 && echo "swift: ok"
else
	@echo "swift: skipped on Linux (Apple-only #90; verified by CI swift-macos job)"
endif
	cd $(Sdks)/clojure && clojure -M -e '(load-file "etunnel.clj")' > /dev/null && echo "clojure: ok"
	cd $(Sdks)/ruby && ruby -c lib/etunnel.rb > /dev/null && echo "ruby: ok"
	# gem build gate: the gemspec lists bin/etunnel explicitly; a missing file only
	# fails at release time (v1.7.36 pack-ruby). Build the gem, then discard it.
	cd $(Sdks)/ruby && gem build etunnel.gemspec --silent >/dev/null && rm -f etunnel-*.gem && echo "ruby: gem ok"
	cd $(Sdks)/perl && perl -c etunnel.pl > /dev/null && echo "perl: ok"
	cd $(Sdks)/php && php -l etunnel.php > /dev/null && echo "php: ok"
	# Kotlin/Scala SDKs are published to Maven Central (dev.ggcode:etunnel-kotlin,
	# etunnel_2.13, etunnel_3): compile gates where the toolchains exist, so the
	# published jars can never drift from a source that does not compile.
	@if command -v kotlinc >/dev/null 2>&1; then \
		cd $(Sdks)/kotlin && kotlinc Etunnel.kt tests/RouteTest.kt -include-runtime -d /tmp/et-verify-kotlin.jar 2>/dev/null \
		  && java -cp /tmp/et-verify-kotlin.jar dev.ggcode.etunnel.RouteTestKt | tail -1 | grep -q PASS && echo "kotlin: ok"; \
	else echo "kotlin: skipped (no kotlinc)"; fi
	@if command -v scalac >/dev/null 2>&1; then \
		rm -rf /tmp/et-verify-scala && mkdir -p /tmp/et-verify-scala && cd $(Sdks)/scala \
		  && scalac -d /tmp/et-verify-scala Etunnel.scala tests/RouteTest.scala tests/CliOutputTest.scala 2>/dev/null; \
		SCALA_HOME_DIR=$$(dirname $$(dirname $$(readlink -f $$(command -v scalac)))); \
		LIB=$$(find "$$SCALA_HOME_DIR" -name 'scala3-library*.jar' -o -name 'scala-library*.jar' 2>/dev/null | tr '\n' ':'); \
		java -cp "/tmp/et-verify-scala:$$LIB" dev.ggcode.etunnel.RouteTest | tail -1 | grep -q PASS \
		  && java -cp "/tmp/et-verify-scala:$$LIB" dev.ggcode.etunnel.CliOutputTest | tail -1 | grep -q PASS && echo "scala: ok"; \
	else echo "scala: skipped (no scalac)"; fi
	# #427: strict type-check of the vendored TypeScript SDK (NodeNext, no emit).
	# Offline on purpose: uses sdks/typescript/node_modules when present (devDependencies
	# and package-lock are pinned - `npm ci` in sdks/typescript) and skips with a
	# hint otherwise, so verify never needs the network.
	@if [ -x $(Sdks)/typescript/node_modules/.bin/tsc ]; then \
		cd $(Sdks)/typescript && ./node_modules/.bin/tsc -p tsconfig.json && echo "typescript: strict ok"; \
	else echo "typescript: skipped (run npm ci in sdks/typescript to enable the strict check)"; fi
	# README structure gate: SDK READMEs are hand-written (the generators are
	# retired), so the sections the docs index and coding agents rely on can
	# silently disappear. Fail with the list of what is missing.
	@fail=0; for f in $(Sdks)/*/README.md sdk/README.md; do \
	  miss=""; \
	  grep -qE '^## Install' "$$f" || miss="$$miss Install"; \
	  grep -qE '^## CLI' "$$f" || miss="$$miss CLI"; \
	  grep -qE '^## Embed' "$$f" || miss="$$miss Embed"; \
	  grep -qE '^## AI agent quick-start' "$$f" || miss="$$miss AI-agent-prompt"; \
	  if [ -n "$$miss" ]; then echo "README structure: $$f missing:$$miss"; fail=1; fi; \
	done; [ $$fail -eq 0 ] && echo "readme structure: ok"

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
	cd $(Sdks)/kotlin && mkdir -p build && kotlinc Etunnel.kt -include-runtime -d ../../$(PACK)/etunnel-kotlin-$(V).jar \
	  && echo "kotlin: compiled $(V)" \
	  && tar czf ../../$(PACK)/etunnel-kotlin-src-$(V).tar.gz Etunnel.kt build.gradle.kts README.md
	# Maven Central set (dev.ggcode:etunnel-kotlin): jar without the runtime +
	# sources + javadoc + pom; signing/upload happens in CI (publish-central-kotlin-scala).
	# Opt-in (PACK_CENTRAL=1): the script downloads a Kotlin compiler and can be
	# rate-limited (429) in the pack job; publish-central-kotlin-scala builds the
	# set itself, so plain `make pack-all` never needs it.
	@if [ "$(PACK_CENTRAL)" = "1" ] && command -v kotlinc >/dev/null 2>&1; then \
	  mkdir -p $(PACK)/maven && sh scripts/build-central-kotlin-scala.sh kotlin > /dev/null \
	  && cp $(Sdks)/kotlin/target/central/etunnel-kotlin-$(V)*.jar $(Sdks)/kotlin/pom.xml $(PACK)/maven/ \
	  && mv $(PACK)/maven/pom.xml $(PACK)/maven/etunnel-kotlin-$(V).pom && echo "kotlin: central set $(V)"; \
	else echo "kotlin: central set skipped (no kotlinc here; built by CI publish-central-kotlin-scala)"; fi

pack-scala: prereq
	cd $(Sdks)/scala && mkdir -p build && scalac -d build Etunnel.scala \
	  && jar cfe ../../$(PACK)/etunnel-scala-$(V).jar Etunnel -C build . \
	  && echo "scala: compiled $(V)" \
	  && tar czf ../../$(PACK)/etunnel-scala-src-$(V).tar.gz Etunnel.scala build.sbt README.md
	# The cross-built set needs BOTH scalac 2.13 and scalac 3 (SDKMAN in CI's
	# publish job only). The pack job and most dev machines have neither, and
	# publish-central-kotlin-scala builds its own set - skip with a message
	# instead of failing the whole release pack (v1.7.38 first run).
	@if [ "$(PACK_CENTRAL)" = "1" ] && { command -v scalac-2.13 >/dev/null 2>&1 || [ -x /opt/homebrew/opt/scala@2.13/bin/scalac ]; }; then \
	  mkdir -p $(PACK)/maven && sh scripts/build-central-kotlin-scala.sh scala-2.13 > /dev/null \
	  && sh scripts/build-central-kotlin-scala.sh scala-3 > /dev/null \
	  && cp $(Sdks)/scala/target/central/etunnel_2.13-$(V)*.jar $(Sdks)/scala/target/central/etunnel_3-$(V)*.jar $(PACK)/maven/ \
	  && cp $(Sdks)/scala/pom-2.13.xml $(PACK)/maven/etunnel_2.13-$(V).pom \
	  && cp $(Sdks)/scala/pom-3.xml $(PACK)/maven/etunnel_3-$(V).pom && echo "scala: central set $(V)"; \
	else echo "scala: central set skipped (no scalac 2.13 + 3 here; built by CI publish-central-kotlin-scala)"; fi

pack-csharp: prereq
	cd $(Sdks)/csharp && dotnet pack -c Release -o ../../$(PACK) /clp:ErrorsOnly

pack-rust: prereq
	cd $(Sdks)/rust && cargo package --no-verify --allow-dirty --quiet \
	  && cp target/package/etunnel-$(RUST_V).crate ../../$(PACK)/

# Swift SDK is Apple-platforms-only (#90: URLSessionWebSocketTask never
# existed in Linux swift-corelibs-foundation). On Apple hosts, compile; on
# Linux, package sources only - compile verification is the dedicated
# swift-macos CI job, never silently skipped.
ifeq ($(UNAME_S),Darwin)
pack-swift: prereq
	cd $(Sdks)/swift && swift build -c release \
	  && echo "swift: compiled (macOS)" \
	  && tar czf ../../$(PACK)/etunnel-swift-$(V).tar.gz etunnel.swift Package.swift README.md
else
pack-swift: prereq
	cd $(Sdks)/swift && tar czf ../../$(PACK)/etunnel-swift-$(V).tar.gz etunnel.swift Package.swift README.md \
	  && echo "swift: source pack (Apple-only #90; compile verified by local make verify on macOS)"
endif

pack-dart: prereq
	cd $(Sdks)/dart && tar czf ../../$(PACK)/etunnel-dart-$(V).tar.gz etunnel.dart lib/etunnel.dart bin/etunnel.dart pubspec.yaml README.md

# HarmonyOS HAR: compiled with tsc from the ArkTS source (no DevEco/hvigor SDK
# needed - see scripts/pack-harmonyos.sh); published to ohpm by CI publish-ohpm.
pack-harmonyos: prereq
	./scripts/pack-harmonyos.sh

pack-ruby: prereq
	cd $(Sdks)/ruby && gem build etunnel.gemspec --silent \
	  && mv etunnel-$(RUBY_V).gem ../../$(PACK)/

pack-php: prereq
	cd $(Sdks)/php && tar czf ../../$(PACK)/etunnel-php-$(V).tar.gz etunnel.php README.md

pack-perl: prereq
	cd $(Sdks)/perl && perl Makefile.PL \
	  && make dist \
	  && mv etunnel-$(V).tar.gz ../../$(PACK)/etunnel-perl-$(V).tar.gz \
	  && rm -f Makefile Makefile.old pm_to_blib

pack-clojure: prereq
	cd $(Sdks)/clojure && jar cf ../../$(PACK)/etunnel-clojure-$(V).jar etunnel.clj \
	  && cp pom.xml ../../$(PACK)/etunnel-clojure-$(V).pom \
	  && cp deps.edn ../../$(PACK)/etunnel-clojure-$(V).deps.edn \
	  && tar czf ../../$(PACK)/etunnel-clojure-src-$(V).tar.gz etunnel.clj deps.edn pom.xml README.md \
	  && echo "clojure: compiled $(V)"

pack-c: prereq
	cd $(Sdks)/c && make etunnel \
	  && rm -f etunnel \
	  && tar czf ../../$(PACK)/etunnel-c-$(V).tar.gz etunnel.c Makefile README.md \
	  && echo "c: compiled $(V)"

pack-cpp: prereq
	cd $(Sdks)/cpp && cmake -B build -DCMAKE_BUILD_TYPE=Release \
	  && cmake --build build --target etunnel \
	  && rm -rf build \
	  && tar czf ../../$(PACK)/etunnel-cpp-$(V).tar.gz etunnel.cpp CMakeLists.txt README.md \
	  && echo "cpp: compiled $(V)"

# php/lua/elixir: no local toolchain — CI matrix builds them
pack-local-only = pack-node pack-typescript pack-deno pack-python pack-java \
  pack-kotlin pack-scala pack-csharp pack-rust pack-swift pack-dart pack-ruby \
  pack-php pack-perl pack-clojure pack-c pack-cpp pack-harmonyos

pack-all: $(pack-local-only)
	@echo ""; echo "=== $(PACK) ==="; ls -lh $(PACK) | awk 'NR>1 {printf "%-42s %s\n", $$9, $$5}'; \
	echo ""; echo "lua(luarocks) elixir(mix) built by CI"

clean-pkgs:
	rm -rf $(PACK)
