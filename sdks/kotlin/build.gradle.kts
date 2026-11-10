plugins {
    kotlin("jvm") version "1.9.24"
}

group = "com.ggcode"
version = "1.4.0"

repositories { mavenCentral() }

tasks.jar {
    manifest { attributes("Main-Class" to "EtunnelKt") }
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    from(configurations.runtimeClasspath.get().map { if (it.isDirectory) it else zipTree(it) })
}
