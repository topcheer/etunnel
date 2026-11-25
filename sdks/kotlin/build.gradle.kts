plugins {
    kotlin("jvm") version "2.2.0"
}

// The published artifact is dev.ggcode:etunnel-kotlin (sdks/kotlin/pom.xml, Maven
// Central); this scratch build stays for local `gradle build` checks.
group = "dev.ggcode"
// read from the repo's VERSION file so it cannot drift from a release
version = file("../../VERSION").readText().trim()

kotlin {
    jvmToolchain(17)
}

// The SDK is a single file at the repo root (the Makefile's packaging step
// and the generated READMEs reference `kotlinc Etunnel.kt` there, so the
// location is load-bearing). example/ and tests/ are user-facing snippets,
// not part of the artifact. Without this block gradle built NOTHING
// (NO-SOURCE) - the SDK had no real compile verification (#214).
sourceSets {
    main {
        kotlin.srcDir(".")
        kotlin.exclude("example/**", "tests/**", "build/**")
    }
}

repositories { mavenCentral() }

tasks.jar {
    manifest { attributes("Main-Class" to "dev.ggcode.etunnel.EtunnelKt") }
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    from(configurations.runtimeClasspath.get().map { if (it.isDirectory) it else zipTree(it) })
}
