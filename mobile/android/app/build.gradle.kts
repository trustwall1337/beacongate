// :app module — the BeaconGate Android client.
//
// Size-targeted v1: arm64-only, ProGuard/R8 enabled in release,
// minimal dependency surface. Matches the plan's "≤ 20 MB APK"
// goal. AAR is sourced from `app/libs/beacongate.aar` (produced by
// `make android-aar` at the repo root).

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.beacongate"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.beacongate"
        minSdk = 24          // Android 7.0+ — covers ~99% of in-use devices
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"

        // ABI filtering is configured below in `splits.abi` (which
        // produces per-ABI APKs). Setting `ndk.abiFilters` here
        // additionally would conflict with that block at configure
        // time. Keeping all ABI policy in one place.
        //
        // R8 needs to keep the gomobile-generated `bindings.*`
        // classes intact — they're called via JNI reflection by the
        // Go runtime. proguard-rules.pro spells out the exact
        // -keep rules.
    }

    buildFeatures {
        viewBinding = true   // typed view access without findViewById
    }

    buildTypes {
        getByName("release") {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
            // Debuggable=false in release; signing is left to a
            // later step (we ship via Drive with operator-signed
            // APKs, not Play Store).
        }
        getByName("debug") {
            // Faster iteration; no shrinking.
            isMinifyEnabled = false
        }
    }

    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a")
            isUniversalApk = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    // Vendored .aar produced by `make android-aar`. Treat it as a
    // regular flatDir source; libs/ is excluded from the standard
    // ignore patterns in the project's .gitignore so a built AAR
    // is *not* tracked in the repo (we want to rebuild from source
    // on each release, not vendor the binary).
    sourceSets {
        getByName("main") {
            jniLibs.srcDirs("libs")
        }
    }
}

dependencies {
    // The gomobile-bound BeaconGate engine. Built by `make
    // android-aar` from the mobile/bindings package and dropped
    // into app/libs/. fileTree pulls it in without us hard-coding
    // the file name.
    implementation(fileTree("libs") { include("*.aar") })

    // Bare-minimum Android dependencies. Each line is justified;
    // anything not on this list adds APK size for no v1 benefit.
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.8.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.1")
    // NOTE: previously pulled androidx.security:security-crypto:1.1.0-alpha06
    // for EncryptedSharedPreferences. Removed in v1 — that alpha
    // crashes native-side on Samsung Android 12/13 during Keystore
    // init (see CredentialStore.kt for the trade-off rationale).
    // Material3 brings the standard color/typography theme; using
    // a thin theme keeps the dependency cost low (~500 KB after R8).
    implementation("com.google.android.material:material:1.12.0")

    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.6.1")
}
