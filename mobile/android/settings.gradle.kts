// Top-level project + module wiring for the BeaconGate Android app.
//
// Single-module project for v1: just `:app`. Future iterations may
// split out a `:bindings-aar` module if we add CI artifacts beyond
// the bundled .aar, but a flat layout keeps the v1 build short.

pluginManagement {
    repositories {
        google {
            content {
                // AGP and Android tooling.
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    // FAIL_ON_PROJECT_REPOS forces all dependencies to come from the
    // repos declared here, NOT from per-project repository{} blocks.
    // Reproducible builds: we know exactly where every artifact came
    // from at audit time.
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "BeaconGate"
include(":app")
