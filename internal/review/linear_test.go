package review

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// The scanners read repository content, which can be hostile (a stranger's
// PR, or a file Claude wrote under prompt injection), and they run on every
// Overview poll. Each case below is an input shape that used to make one of
// them quadratic, so that a 2 MB file took minutes and gigabytes. The test
// scans the input at size n and 4n: linear work grows about 4×, quadratic
// 16×. docs/plans/review-remediation.md, step 1.4.

type linearCase struct {
	name string
	gen  func(n int) string
	scan func(src string)
	n    int
}

func linearCases() []linearCase {
	rep := func(unit string) func(int) string {
		return func(n int) string { return strings.Repeat(unit, n) }
	}
	js := func(src string) { (&jsCalls{}).scanFile("x.ts", src); jsImports(jsCode(src)) }
	jvm := func(src string) { (&jvmCalls{}).scanFile("X.kt", src); jvmRefs(src) }
	java := func(src string) { (&jvmCalls{}).scanFile("X.java", src) }
	swift := func(src string) { (&swiftCalls{}).scanFile("x.swift", src); swiftRefs(src) }
	ruby := func(src string) { (&rbCalls{}).scanFile("x.rb", src); rubyConstants(rubyCode(src)) }
	py := func(src string) { (&pyCalls{}).scanFile("x.py", src); pyImports(src) }
	return []linearCase{
		{"js empty block comments", rep("/**/"), js, 20_000},
		{"js chain", func(n int) string { return strings.Repeat("a.", n) + "a()\n" }, js, 5_000},
		{"kotlin triple quotes", rep(`""""""` + "\n"), jvm, 20_000},
		{"java open generics", rep("a<"), java, 5_000},
		{"java open parens", func(n int) string { return "class X {\n" + strings.Repeat("Type name(\n", n) }, java, 3_000},
		{"jvm chain", func(n int) string { return "fun f() {\n" + strings.Repeat("a.", n) + "a()\n}\n" }, jvm, 5_000},
		{"swift raw strings", rep(`#""#` + "\n"), swift, 20_000},
		{"swift triple quotes", rep(`""""""` + "\n"), swift, 20_000},
		{"swift bodiless funcs", rep("func f()\n"), swift, 20_000},
		{"ruby begin/end", rep("=begin\n=end\n"), ruby, 10_000},
		{"ruby macro open bracket", func(n int) string {
			return "class A < ApplicationRecord\n" + strings.Repeat("after_save :a, (\n", n)
		}, ruby, 1_000},
		{"ruby nested modules", rep("module A\n"), ruby, 2_000},
		{"ruby constant path", func(n int) string { return "X = " + strings.Repeat("A::", n) + "A\n" }, ruby, 5_000},
		{"ruby constant lookup", func(n int) string { return strings.Repeat("A::", n) + "A" }, func(src string) {
			l := &rubyLang{consts: map[string]string{"a": "app/models", "a/b": "app/models"}, files: map[string]string{"a": "app/models/a.rb"}, depth: 2}
			l.lookup(src, []string{"x/y", "x", ""})
			l.constFile(src)
		}, 20_000},
		{"erb tags", rep("<%%>"), func(src string) { erbCode(src) }, 20_000},
		{"python open paren", func(n int) string { return "x = (\n" + strings.Repeat("y\n", n) }, py, 5_000},
	}
}

func timeScan(scan func(string), src string) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < 3; i++ {
		start := time.Now()
		scan(src)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// allocBytes is how many bytes one scan of src allocates.
func allocBytes(scan func(string), src string) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	scan(src)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestScannersLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	if raceEnabled {
		t.Skip("timing test: run without -race (CI has a step for it)")
	}
	for _, c := range linearCases() {
		t.Run(c.name, func(t *testing.T) {
			// 4× the input: linear is ~4×; allow noise, refuse 16×. A
			// bound of 9× let a scan through that grew 3.4× per doubling. A
			// quadratic scan is over the bound every time, while a busy
			// machine (the whole suite running in parallel) only now and
			// then, so it fails only if three measurements all are.
			smallSrc, bigSrc := c.gen(c.n), c.gen(4*c.n)
			var small, big time.Duration
			for try := 0; try < 3; try++ {
				small, big = timeScan(c.scan, smallSrc), timeScan(c.scan, bigSrc)
				if big <= 7*small+30*time.Millisecond {
					break
				}
				if try == 2 {
					t.Errorf("n=%d: %v, 4n: %v (%.1f×): not linear", c.n, small, big, float64(big)/float64(small))
				}
			}
			smallB, bigB := allocBytes(c.scan, c.gen(c.n)), allocBytes(c.scan, c.gen(4*c.n))
			if bigB > 9*smallB+(1<<20) {
				t.Errorf("n=%d: %d bytes, 4n: %d bytes (%.1f×): memory not linear", c.n, smallB, bigB, float64(bigB)/float64(smallB))
			}
		})
	}
}

// FuzzScanners: no input crashes a scanner or takes long. A crash here
// would take mo web down (scans run on worker goroutines, outside the
// per-language recover).
//
//	go test ./internal/review -run '^$' -fuzz FuzzScanners
func FuzzScanners(f *testing.F) {
	for _, s := range []string{
		"def f(x):\n    return g(x)\n", "class A < B\n  def f; g; end\nend\n", "export function f() { return g() }\n",
		"fun f() = g()\n", "struct S { func f() { g() } }\n", "/* ", `"""`, "<% x %>", "=begin\n", "(((", "a.b.c(",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		start := time.Now()
		(&pyCalls{}).scanFile("x.py", src)
		pyImports(src)
		(&rbCalls{}).scanFile("x.rb", src)
		rubyConstants(rubyCode(src))
		erbCode(src)
		(&jsCalls{}).scanFile("x.tsx", src)
		jsImports(jsCode(src))
		(&jvmCalls{}).scanFile("X.kt", src)
		(&jvmCalls{}).scanFile("X.java", src)
		jvmRefs(src)
		(&swiftCalls{}).scanFile("x.swift", src)
		swiftRefs(src)
		if d := time.Since(start); d > time.Second && len(src) < 4096 {
			t.Errorf("%d bytes took %v", len(src), d)
		}
	})
}
