package review

import (
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
		{"swift bodiless funcs", rep("func f()\n"), swift, 5_000},
		{"ruby begin/end", rep("=begin\n=end\n"), ruby, 10_000},
		{"ruby macro open bracket", func(n int) string {
			return "class A < ApplicationRecord\n" + strings.Repeat("after_save :a, (\n", n)
		}, ruby, 1_000},
		{"ruby nested modules", rep("module A\n"), ruby, 2_000},
		{"ruby constant path", func(n int) string { return "X = " + strings.Repeat("A::", n) + "A\n" }, ruby, 5_000},
		{"erb tags", rep("<%%>"), func(src string) { erbCode(src) }, 20_000},
		{"python open paren", func(n int) string { return "x = (\n" + strings.Repeat("y\n", n) }, py, 5_000},
	}
}

func timeScan(scan func(string), src string) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < 2; i++ {
		start := time.Now()
		scan(src)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

func TestScannersLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	for _, c := range linearCases() {
		t.Run(c.name, func(t *testing.T) {
			small := timeScan(c.scan, c.gen(c.n))
			big := timeScan(c.scan, c.gen(4*c.n))
			// 4× the input: linear is ~4×; allow noise, refuse 16×.
			if big > 9*small+30*time.Millisecond {
				t.Errorf("n=%d: %v, 4n: %v (%.1f×): not linear", c.n, small, big, float64(big)/float64(small))
			}
		})
	}
}
