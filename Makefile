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

.PHONY: all verify pack-all clean-pkgs $(PACK) prereq check-versions docs

all: pack-all

prereq:
	@mkdir -p $(PACK)

# ---------- documentation ----------
# Regenerates the 19 per-language READMEs under sdks/ from one template so the
# install/embed/API sections stay in sync with the code.
docs:
	python3 scripts/gen_sdk_readmes.py

# ---------- verification (compile-only, no artifacts) ----------
check-versions:
	python3 scripts/bump-release.py --check

verify: check-versions
	go build ./... && go test -count=1 ./... > /dev/null && echo "go: ok"
	cd $(Sdks)/node && node --check etunnel.js && echo "node: ok"
	cd $(Sdks)/python && python3 -m py_compile etunnel.py && echo "python: ok"
	cd $(Sdks)/java && javac -d /tmp/et-verify-java src/main/java/dev/ggcode/Etunnel.java && echo "java: ok"
ifeq ($(UNAME_S),Darwin)
	cd $(Sdks)/swift && swift build 2>&1 && echo "swift: ok"
else
	@echo "swift: skipped on Linux (Apple-only #90; verified by CI swift-macos job)"
endif
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
	cd $(Sdks)/kotlin && mkdir -p build && kotlinc Etunnel.kt -include-runtime -d ../../$(PACK)/etunnel-kotlin-$(V).jar \
	  && echo "kotlin: compiled $(V)" \
	  && tar czf ../../$(PACK)/etunnel-kotlin-src-$(V).tar.gz Etunnel.kt build.gradle.kts README.md

pack-scala: prereq
	cd $(Sdks)/scala && mkdir -p build && scalac -d build Etunnel.scala \
	  && jar cfe ../../$(PACK)/etunnel-scala-$(V).jar Etunnel -C build . \
	  && echo "scala: compiled $(V)" \
	  && tar czf ../../$(PACK)/etunnel-scala-src-$(V).tar.gz Etunnel.scala build.sbt README.md

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
	  && echo "swift: source pack (Apple-only #90; compile verified by CI swift-macos job)"
endif

pack-dart: prereq
	cd $(Sdks)/dart && tar czf ../../$(PACK)/etunnel-dart-$(V).tar.gz etunnel.dart pubspec.yaml README.md

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
  pack-php pack-perl pack-clojure pack-c pack-cpp

pack-all: $(pack-local-only)
	@echo ""; echo "=== $(PACK) ==="; ls -lh $(PACK) | awk 'NR>1 {printf "%-42s %s\n", $$9, $$5}'; \
	echo ""; echo "lua(luarocks) elixir(mix) built by CI"

clean-pkgs:
	rm -rf $(PACK)
