# ProGuard / R8 rules for the BeaconGate Android app.
#
# Most rules are inherited from the AGP-default
# proguard-android-optimize.txt; this file holds the BeaconGate-
# specific keep rules.

# --- gomobile-generated bridge classes ----------------------------------
# The .aar produced by `gomobile bind` contains classes under
# `bindings` (the Java package gomobile derives from the last
# component of the Go import path — our Go package is named
# `bindings`, so the Java side is `bindings.*`). The Go runtime
# calls into these via reflection / JNI; R8 must NOT shrink them.
# Without this rule the app crashes at the first call into the
# facade.
-keep class bindings.** { *; }
-keep interface bindings.** { *; }

# gomobile's runtime support classes (under go.* — these are
# infrastructure shared across all gomobile-bound libraries).
-keep class go.Seq { *; }
-keep class go.Seq$* { *; }
-keep class go.Universe { *; }
-keep class go.Universe$* { *; }
-keep class go.error { *; }

# Annotations the gomobile-bound interface uses for the LogSink
# callback (Bindings.SetLogSink). Keeping them lets the interface
# survive obfuscation.
-keepattributes Signature, InnerClasses, EnclosingMethod

# --- Nothing else for v1 ------------------------------------------------
# AppCompat, Material, AndroidX security, lifecycle: all ship with
# their own consumer-rules.pro inside the .aar; AGP picks those up
# automatically. Don't duplicate them here.
