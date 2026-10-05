package review

import (
	"os/exec"
	"reflect"
	"testing"
)

// ktRepo is a three-module Android app (:app, :core:data, :core:model,
// with a Java source set); branch "feat" adds a wildcard import that
// breaks the android preset, a qualified reference, an endpoint, a
// permission, an exported activity and dependency bumps.
func ktRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "settings.gradle.kts", "rootProject.name = \"acme\"\ninclude(\":app\", \":core:data\")\ninclude(\":core:model\")\n")
	write(t, dir, "app/build.gradle.kts", "android {\n  defaultConfig {\n    minSdk = 26\n  }\n}\ndependencies {\n  implementation(\"com.squareup.retrofit2:retrofit:2.9.0\")\n}\n")
	write(t, dir, "gradle/libs.versions.toml", "[versions]\ncompose = \"1.6.0\"\n\n[libraries]\ncompose-ui = { module = \"androidx.compose.ui:ui\", version.ref = \"compose\" }\n")
	write(t, dir, "app/src/main/AndroidManifest.xml", "<manifest>\n  <uses-permission android:name=\"android.permission.INTERNET\" />\n  <application>\n    <activity android:name=\".MainActivity\" android:exported=\"true\" />\n  </application>\n</manifest>\n")
	write(t, dir, "app/src/main/kotlin/com/acme/app/MainActivity.kt", "package com.acme.app\n\nimport com.acme.core.data.Repo\n\nclass MainActivity\n")
	write(t, dir, "app/src/main/kotlin/com/acme/app/ui/HomeScreen.kt", "package com.acme.app.ui\n\nfun HomeScreen() {}\n")
	write(t, dir, "core/data/src/main/kotlin/com/acme/core/data/Repo.kt", "package com.acme.core.data\n\nclass Repo\n")
	write(t, dir, "core/data/src/main/java/com/acme/core/data/remote/Api.java", "package com.acme.core.data.remote;\n\ninterface Api {\n  @GET(\"artworks\") Object artworks();\n}\n")
	write(t, dir, "core/model/src/main/kotlin/com/acme/core/model/Artwork.kt", "package com.acme.core.model\n\ndata class Artwork(val title: String)\n")
	write(t, dir, "core/model/src/test/kotlin/com/acme/core/model/ArtworkTest.kt", "package com.acme.core.model\n\nimport com.acme.app.MainActivity\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "core/model/src/main/kotlin/com/acme/core/model/Artwork.kt", "package com.acme.core.model\n\nimport com.acme.app.ui.*\n\n/* com.acme.core.data.Repo in a comment */\ndata class Artwork(val title: String)\n")
	write(t, dir, "core/data/src/main/kotlin/com/acme/core/data/Repo.kt", "package com.acme.core.data\n\nclass Repo {\n  fun first() = com.acme.core.model.Artwork(\"x\")\n}\n")
	write(t, dir, "core/data/src/main/java/com/acme/core/data/remote/Api.java", "package com.acme.core.data.remote;\n\ninterface Api {\n  @GET(\"artworks\") Object artworks();\n  @GET(\"events\") Object events();\n  // @POST(\"commented\")\n}\n")
	write(t, dir, "app/src/main/AndroidManifest.xml", "<manifest>\n  <uses-permission android:name=\"android.permission.INTERNET\" />\n  <uses-permission android:name=\"android.permission.CAMERA\" />\n  <application>\n    <activity android:name=\".MainActivity\" android:exported=\"true\" />\n    <activity\n      android:exported=\"true\"\n      android:name=\".Share\" />\n  </application>\n</manifest>\n")
	write(t, dir, "app/build.gradle.kts", "android {\n  defaultConfig {\n    minSdk = 28\n  }\n}\ndependencies {\n  implementation(\"com.squareup.retrofit2:retrofit:2.11.0\")\n}\n")
	write(t, dir, "gradle/libs.versions.toml", "[versions]\ncompose = \"1.7.0\"\n\n[libraries]\ncompose-ui = { module = \"androidx.compose.ui:ui\", version.ref = \"compose\" }\n")
	return dir
}

func TestKotlinArchitecture(t *testing.T) {
	a := analyze(t, ktRepo(t))
	if got := edgeKeys(a.Edges); !reflect.DeepEqual(got, []string{"+core/data>core/model", "+core/model>app/ui"}) {
		t.Errorf("edges %v", got)
	}
	// A file's unit is its module and package, not its folder.
	if u := a.FileUnits["core/data/src/main/kotlin/com/acme/core/data/Repo.kt"]; u != "core/data" {
		t.Errorf("Repo.kt's unit %q", u)
	}
	for _, e := range a.Edges {
		if e.Lang != "kotlin" || e.Approx {
			t.Errorf("edge %+v", e)
		}
		if e.From == "core/model" && e.Violation != "android: model and domain must not import app/ui" {
			t.Errorf("model → ui: %+v", e)
		}
	}
	if a.Violations != 1 {
		t.Errorf("violations %d, rules %+v", a.Violations, a.Rules)
	}
}

func TestKotlinUnits(t *testing.T) {
	l := &ktLang{rootPkg: map[string]string{
		"app/src/main/kotlin":    "com/acme/app",
		"app/src/staging/kotlin": "com/acme/app",
		"src/main/kotlin":        "me/x",
	}}
	for p, want := range map[string]string{
		"app/src/main/kotlin/com/acme/app/MainActivity.kt":       "app",
		"app/src/main/kotlin/com/acme/app/ui/home/HomeScreen.kt": "app/ui",
		"app/src/staging/kotlin/com/acme/app/debug/Tools.kt":     "app/staging/debug",
		"src/main/kotlin/me/x/data/Repo.kt":                      "data", // a root-level module
		"app/build.gradle.kts":                                   "",
	} {
		if got := l.unit(p); got != want {
			t.Errorf("unit(%s) = %q, want %q", p, got, want)
		}
	}
}

func TestAndroidSurface(t *testing.T) {
	s := analyze(t, ktRepo(t)).Surface
	if got := changeKeys(s.Permissions); !reflect.DeepEqual(got, []string{"+android.permission.CAMERA", "+exported activity .Share"}) {
		t.Errorf("permissions %v", got)
	}
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+GET events"}) {
		t.Errorf("routes %v", got)
	}
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"~androidx.compose.ui:ui", "~com.squareup.retrofit2:retrofit", "~minSdk"}) {
		t.Errorf("deps %+v", s.Deps)
	}
}
