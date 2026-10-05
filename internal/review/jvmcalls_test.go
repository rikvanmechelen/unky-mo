package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// scanDefs summarizes a scan: qualified name → its calls and range.
func scanDefs(f hFile) map[string]string {
	got := map[string]string{}
	for _, d := range f.Defs {
		var calls []string
		for _, c := range d.Calls {
			k := c.Name
			if c.Recv != "" {
				k = c.Recv + "." + k
			}
			if c.Ref {
				k += "@ref"
			}
			calls = append(calls, k)
		}
		got[qualify(d.Owner, d.Name)] = strings.Join(calls, " ") + rangeOf(d)
	}
	return got
}

func TestKotlinScanDefs(t *testing.T) {
	src := `package com.acme.data

import com.acme.util.format
import com.acme.util.Helper as H
import com.acme.model.*

/* fake() in /* a nested */ comment */
open class Base(val repo: Repo) : Parent(), Iface {
    private val cache = Cache()

    fun load(id: String): Item {
        val s = "nope() in a string"
        return repo.find(id).also { log(it) }
    }

    fun <T> List<T>.second(): T = this[1]

    companion object {
        fun create() = Base(Repo())
    }
}

fun Item.label(): String =
    format(name)
        .trim()

@Composable
fun Screen(items: List<Item>) {
    Column {
        items.forEach { Row(it) }
    }
    val ref = ::helper
}

interface Source {
    fun fetch(id: String): Item
}

object Registry {
    fun register() = H.add(Base::create)
}
`
	f := (&jvmCalls{}).scanFile("x.kt", src)
	if !f.OK {
		t.Fatal("not OK")
	}
	want := map[string]string{
		"Base":              "Cache [8-21]",
		"Base.load":         "Repo.find ?.also log [11-14]", // repo is a constructor property
		"Base.second":       " [16-16]",
		"Base.create":       "Base Repo [19-19]", // a companion's member is the class's
		"label":             "format ?.trim [23-25]",
		"Screen":            "Column List.forEach Row helper@ref [28-33]",
		"Source":            " [35-37]",
		"Source.fetch":      " [36-36]",
		"Registry":          " [39-41]",
		"Registry.register": "H.add Base.create@ref [40-40]",
	}
	if got := scanDefs(f); !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	for _, d := range f.Defs {
		switch d.Name {
		case "create", "register":
			if !d.Static {
				t.Errorf("%s not static", d.Name)
			}
		case "label":
			if jvmExtRecv(&d) != "Item" {
				t.Errorf("label receiver %q", jvmExtRecv(&d))
			}
		case "second":
			if jvmExtRecv(&d) != "List" {
				t.Errorf("second receiver %q", jvmExtRecv(&d))
			}
		case "load":
			if jvmExtRecv(&d) != "" || d.Static {
				t.Errorf("load %+v", d)
			}
		}
	}
	if len(f.Classes) != 3 || !reflect.DeepEqual(f.Classes[0].Bases, []string{"Parent", "Iface"}) {
		t.Errorf("classes = %+v", f.Classes)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"format=com.acme.util:format", "H=com.acme.util:Helper", "*com.acme.model=com.acme.model:*"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
}

func TestJavaScanDefs(t *testing.T) {
	src := `package com.acme.legacy;

import static com.acme.util.Strings.join;
import com.acme.data.*;

public class Exporter extends BaseExporter implements Runnable, Closeable {
    private final Repo repo;
    private static final Exporter INSTANCE = new Exporter(null);

    public Exporter(Repo repo) {
        this.repo = repo;
    }

    @Override
    public void run() {
        String s = "fake()" + new ArrayList<>(); // nope() in a comment
        repo.load(s);
        helper(Exporter::format);
        new Thread(new Runnable() {
            public void run() { inner(); }
        }).start();
    }

    static <T> List<T> format(T x) throws IOException {
        return join(x);
    }

    abstract void todo();

    class Inner {
        Inner() { super(); }
    }
}
`
	f := (&jvmCalls{}).scanFile("x.java", src)
	if !f.OK {
		t.Fatal("not OK")
	}
	want := map[string]string{
		"Exporter":              "Exporter [6-33]",
		"Exporter.<init>":       " [10-12]",
		"Exporter.run":          "ArrayList Repo.load helper Exporter.format@ref Thread Runnable inner ?.start [15-22]",
		"Exporter.format":       "join [24-26]",
		"Exporter.todo":         " [28-28]",
		"Exporter.Inner":        " [30-32]",
		"Exporter.Inner.<init>": " [31-31]",
	}
	if got := scanDefs(f); !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	for _, d := range f.Defs {
		if (d.Name == "format") != d.Static {
			t.Errorf("%s static = %v", d.Name, d.Static)
		}
	}
	if len(f.Classes) != 2 || !reflect.DeepEqual(f.Classes[0].Bases, []string{"BaseExporter", "Runnable", "Closeable"}) {
		t.Errorf("classes = %+v", f.Classes)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"join=com.acme.util.Strings:join", "*com.acme.data=com.acme.data:*"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
}

// A file mid-edit (any prefix of one) scans without hanging or panicking.
func TestJvmScanPrefixes(t *testing.T) {
	kt := "package a\n\nclass A @Inject constructor(val r: R) : B(x()), C by d {\n  fun <T> T.f(): Int where T : Any = g {\n    h(\"}\")\n  }\n  constructor(x: Int) : this(R()) { i() }\n}\n"
	java := "package a;\n\nclass A<T> extends B<T> implements C {\n  @X(\"y\") A(int x) throws E { super(x); }\n  static <U> U f(U u) { return new A<>(1).g(u::h); }\n  enum K { P(1), Q; }\n}\n"
	for _, src := range []struct{ path, text string }{{"x.kt", kt}, {"x.java", java}} {
		for i := range src.text {
			(&jvmCalls{}).scanFile(src.path, src.text[:i])
		}
		if f := (&jvmCalls{}).scanFile(src.path, src.text); !f.OK || len(f.Defs) < 3 {
			t.Errorf("%s: %+v", src.path, f)
		}
	}
}

// Overloads are one definition: a change to either one's parameters is a
// signature change, and a body is only its own lines.
func TestJvmScanOverloadsAndBodies(t *testing.T) {
	scan := func(src string) map[string]hDef {
		f := (&jvmCalls{}).scanFile("x.kt", src)
		out := map[string]hDef{}
		for _, d := range f.Defs {
			out[qualify(d.Owner, d.Name)] = d
		}
		return out
	}
	a := scan("class A {\n  fun f(x: Int) = g(x)\n  fun f(x: String) { h() }\n}\n")
	b := scan("class A {\n  fun f(x: Int) = g(x)\n  fun f(x: Long) { h() }\n}\n")
	c := scan("class A {\n  fun f(x: Int) =   g(x) // a comment\n  /* another */\n  fun f(x: String) { h() }\n}\n")
	d := scan("class A {\n  fun f(x: Int) = g(x)\n  fun f(x: String) { h(\"s\") }\n}\n")
	e := scan("class A {\n  fun f(x: Int) = g(x)\n  fun f(x: String) { h() }\n  fun k() {}\n}\n")
	if len(a) != 2 || len(a["A.f"].Calls) != 2 {
		t.Fatalf("defs %+v", a)
	}
	if a["A.f"].Sig == b["A.f"].Sig {
		t.Error("an overload's parameters changed but the signature didn't")
	}
	if a["A.f"].Body != c["A.f"].Body {
		t.Error("a comment or spacing changed the body")
	}
	if a["A.f"].Body == d["A.f"].Body {
		t.Error("a new string literal didn't change the body")
	}
	if a["A"].Body != b["A"].Body || a["A"].Body != d["A"].Body || a["A"].Body != e["A"].Body {
		t.Error("a class's body changed with its methods'")
	}
	unbalanced := (&jvmCalls{}).scanFile("x.kt", "class A {\n  fun f() {\n}\n")
	if unbalanced.OK {
		t.Error("an unclosed class parsed")
	}
}

// jvmCallRepo: a Gradle app with Kotlin and Java sources and a test,
// changed on a branch.
func jvmCallRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	const data = "app/src/main/kotlin/com/acme/app/data/"
	write(t, dir, "settings.gradle.kts", "rootProject.name = \"acme\"\ninclude(\":app\")\n")
	write(t, dir, "app/build.gradle.kts", "android {}\n")
	write(t, dir, data+"Artwork.kt", `package com.acme.app.data

open class Model {
    fun validate(): Boolean = true
}

data class Artwork(val title: String) : Model() {
    fun save() {
        validate()
    }

    fun gone() {}

    companion object {
        fun create(title: String) = Artwork(title)
    }
}

fun Artwork.label(): String = title.uppercase()
`)
	write(t, dir, data+"Format.kt", "package com.acme.app.data\n\n// fake() in a comment\nfun format(s: String) = s.trim()\n\nfun fake() {}\n")
	write(t, dir, data+"Repo.kt", `package com.acme.app.data

class Repo {
    fun load(title: String): Artwork {
        val a = Artwork.create(title)
        a.save()
        return a
    }

    fun legacy(a: Artwork) = a.gone()
}
`)
	write(t, dir, "app/src/main/kotlin/com/acme/app/ui/HomeScreen.kt", `package com.acme.app.ui

import androidx.compose.runtime.Composable
import com.acme.app.data.Repo

@Composable
fun HomeScreen(repo: Repo) {
    val a = repo.load("fake()")
    Column {
        Title(a.title)
    }
}

@Composable
fun Title(text: String) {
    Text(text)
}
`)
	write(t, dir, "app/src/main/java/com/acme/app/legacy/Exporter.java", `package com.acme.app.legacy;

import com.acme.app.data.Artwork;
import com.acme.app.data.Repo;

public class Exporter {
    private final Repo repo;

    public Exporter(Repo repo) {
        this.repo = repo;
    }

    public String export(String title) {
        Artwork a = repo.load(title);
        return title;
    }

    public String label() {
        return "x";
    }
}
`)
	write(t, dir, "app/src/test/kotlin/com/acme/app/data/RepoTest.kt", `package com.acme.app.data

import org.junit.Test

class RepoTest {
    @Test
    fun loads() {
        Repo().load("x")
    }
}
`)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, data+"Artwork.kt", `package com.acme.app.data

open class Model {
    fun validate(): Boolean = true
}

data class Artwork(val title: String) : Model() {
    fun save() {
        validate()
        format(title)
    }

    fun share() = label()

    companion object {
        fun create(title: String) = Artwork(title)
    }
}

fun Artwork.label(): String = title.uppercase()
`)
	write(t, dir, data+"Repo.kt", `package com.acme.app.data

import com.acme.app.legacy.Exporter

class Repo {
    fun load(title: String, fresh: Boolean): Artwork {
        val a = Artwork.create(title)
        a.save()
        return a
    }

    fun legacy(a: Artwork) = a.gone()

    fun title(a: Artwork) = a.label()

    fun all() = listOf("a").map(::format)

    fun exporter() = Exporter(this)
}
`)
	write(t, dir, "app/src/main/kotlin/com/acme/app/ui/HomeScreen.kt", `package com.acme.app.ui

import androidx.compose.runtime.Composable
import com.acme.app.data.Repo
import com.acme.app.data.format

@Composable
fun HomeScreen(repo: Repo) {
    val a = repo.load("fake()")
    Column {
        Title(a.title)
        Footer()
    }
}

@Composable
fun Title(text: String) {
    Text(text)
}

@Composable
fun Footer() {
    Text(format("(c)"))
}
`)
	return dir
}

func TestJvmCalls(t *testing.T) {
	cg := callGraphOf(t, jvmCallRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	const d, ui = "com.acme.app.data.", "com.acme.app.ui."
	want := map[string]string{
		d + "Artwork.save":  FuncChanged,
		d + "Artwork.gone":  FuncRemoved,
		d + "Artwork.share": FuncAdded,
		d + "Repo.load":     FuncSignature,
		d + "Repo.title":    FuncAdded,
		d + "Repo.all":      FuncAdded,
		d + "Repo.exporter": FuncAdded,
		ui + "HomeScreen":   FuncChanged,
		ui + "Footer":       FuncAdded,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+" + d + "Artwork.save>" + d + "format@approx",                      // the same package, no import
		d + "Artwork.save>" + d + "Model.validate@approx",                    // up the class chain
		"+" + d + "Artwork.share>" + d + "label@approx",                      // an extension on this
		"+" + d + "Repo.title>" + d + "label@approx",                         // an extension on a typed parameter
		"+" + d + "Repo.all>" + d + "format@ref",                             // a method reference
		"+" + d + "Repo.exporter>com.acme.app.legacy.Exporter.<init>@approx", // Kotlin → a Java constructor
		d + "Repo.load>" + d + "Artwork.create@approx",                       // a companion member
		d + "Repo.load>" + d + "Artwork.save@approx",                         // the only save()
		"com.acme.app.legacy.Exporter.export>" + d + "Repo.load@approx",      // Java → Kotlin
		d + "RepoTest.loads>" + d + "Repo.load@approx",                       // a test
		ui + "HomeScreen>" + d + "Repo.load@approx",                          // an imported type
		"+" + ui + "HomeScreen>" + ui + "Footer@approx",                      // a Composable
		ui + "HomeScreen>" + ui + "Title@approx",
		"+" + ui + "Footer>" + d + "format@approx", // an imported function
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s", w)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "fake") {
			t.Errorf("call from a string or comment: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:" + d + "Artwork.gone app/src/main/kotlin/com/acme/app/data/Repo.kt", // a.gone() on a typed parameter
		"signature-callers:" + d + "Repo.load app/src/main/java/com/acme/app/legacy/Exporter.java app/src/test/kotlin/com/acme/app/data/RepoTest.kt",
		"untested:" + d + "Artwork.share",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	if contains(findings, "untested:"+d+"Artwork.save") {
		t.Error("save is reached by a test through load")
	}
	for _, f := range cg.Funcs {
		if f.ID == d+"Artwork.share" && (f.Name != "Artwork.share" || f.Unit != "app/data") {
			t.Errorf("share = %+v", f)
		}
		if f.ID == d+"label" && f.Name != "Artwork.label" {
			t.Errorf("label = %+v", f)
		}
	}
	if len(cg.Languages) != 1 || cg.Languages[0].Name != "kotlin" || cg.Languages[0].Exact {
		t.Errorf("languages %+v", cg.Languages)
	}
}
