// etunnel Android SDK build (#260): Kotlin + OkHttp3 WebSocket client,
// packaged as an AAR on Maven Central (dev.ggcode:etunnel-android).
//
// Local verify:  ./gradlew build   (assembleRelease + unit tests)
// Publish:       ./gradlew publishToMavenCentral  (CI publish-android job,
//                credentials/signing injected via Gradle properties)
plugins {
    id("com.android.library") version "8.7.3"
    kotlin("android") version "2.1.0"
    id("com.vanniktech.maven.publish") version "0.30.0"
}

// Release version for etunnel-android: stamped by scripts/bump-release.py,
// which keeps this line and the coordinates() version below in lockstep.
// CI's sdk-packages publish-android job greps the coordinates() literal for
// the version it idempotency-checks against Maven Central.
version = "1.7.29"

android {
    namespace = "dev.ggcode.etunnel"
    compileSdk = 35

    defaultConfig {
        minSdk = 24
        buildToolsVersion = "35.0.0"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    lint {
        // `build` runs lintVitalRelease; this SDK has no resources/manifest
        // surface beyond the INTERNET permission, don't let lint opinions
        // block protocol work.
        abortOnError = false
    }
}

dependencies {
    // api: consumers touch OkHttp WebSocket/OkHttpClient types when
    // integrating the tunnel into their own lifecycle management.
    api("com.squareup.okhttp3:okhttp:4.12.0")

    // Pure-JVM unit tests (no device/emulator): URL parsing, env mapping,
    // routes, JSON and frame layout.
    testImplementation("junit:junit:4.13.2")
}

mavenPublishing {
    coordinates("dev.ggcode", "etunnel-android", "1.7.29")

    pom {
        name.set("etunnel-android")
        description.set("etunnel Android SDK: expose a local Android service on the public internet over one outbound WebSocket (Kotlin + OkHttp3)")
        inceptionYear.set("2026")
        url.set("https://github.com/topcheer/etunnel")

        licenses {
            license {
                name.set("MIT")
                url.set("https://opensource.org/licenses/MIT")
                distribution.set("https://opensource.org/licenses/MIT")
            }
        }

        developers {
            developer {
                id.set("topcheer")
                name.set("topcheer")
                url.set("https://github.com/topcheer")
            }
        }

        scm {
            url.set("https://github.com/topcheer/etunnel")
            connection.set("scm:git:git://github.com/topcheer/etunnel.git")
            developerConnection.set("scm:git:ssh://github.com/topcheer/etunnel.git")
        }
    }

    // Central Portal target (NOT the 0.30.0 default legacy S01 host — our
    // Portal token only works on the Central Portal API, same channel as
    // the java SDK's central-publishing-maven-plugin; S01 → 402, run
    // 44770180246) + signing; credentials come from
    // mavenCentralUsername/mavenCentralPassword Gradle properties (env
    // ORG_GRADLE_PROJECT_* in CI). Publish tasks are only invoked by the
    // sdk-packages publish-android job on v* tags.
    publishToMavenCentral(com.vanniktech.maven.publish.SonatypeHost.CENTRAL_PORTAL, true)
    signAllPublications()
}
