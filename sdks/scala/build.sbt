ThisBuild / organization := "com.ggcode"
ThisBuild / version := "1.4.0"
ThisBuild / scalaVersion := "2.13.14"

lazy val root = (project in file("."))
  .settings(
    name := "etunnel",
    libraryDependencies ++= Seq(
      "org.scala-lang" % "scala-reflect" % scalaVersion.value % Provided
    ),
    Compile / unmanagedSources := Seq(file("Etunnel.scala")),
    packageOptions := Seq(ManifestAttributes("Main-Class" -> "Etunnel"))
  )
