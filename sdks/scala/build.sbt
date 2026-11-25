ThisBuild / organization := "dev.ggcode"
// read from the repo's VERSION file so it cannot drift from a release (the
// published coordinates are dev.ggcode:etunnel_2.13 / etunnel_3 on Maven Central)
ThisBuild / version := IO.read(file("../../VERSION")).trim
ThisBuild / scalaVersion := "2.13.14"

lazy val root = (project in file("."))
  .settings(
    name := "etunnel",
    libraryDependencies ++= Seq(
      "org.scala-lang" % "scala-reflect" % scalaVersion.value % Provided
    ),
    Compile / unmanagedSources := Seq(file("Etunnel.scala")),
    packageOptions := Seq(ManifestAttributes("Main-Class" -> "dev.ggcode.etunnel.Main"))
  )
