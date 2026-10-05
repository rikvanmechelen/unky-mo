package review

import (
	"reflect"
	"regexp"
	"testing"
)

// The regexps rbChains, rbChainSegs and rbKeywordSpans replace: their
// matches are the specification.
var (
	rbChainSpecRe = regexp.MustCompile(`(?:::)?[A-Za-z_]\w*[?!]?(?:\s*&?\.\s*[A-Za-z_]\w*[?!]?|::[A-Z]\w*)*`)
	rbSegSpecRe   = regexp.MustCompile(`\s*&?\.\s*`)
	rbKwSpecRe    = regexp.MustCompile(`\b(class|module|def|if|unless|while|until|case|begin|for|do|end)\b`)
)

var rbChainSeeds = []string{
	"",
	"x",
	"@ticket.event.name",
	"Ticket.where(id: 1).first&.name",
	"a . b &. c",
	"a.\tb",
	"a. 1",
	"a&b",
	"a&&.b",
	"::Foo::Bar.baz!",
	":::x",
	"a::b",
	"A::b",
	"A::B::c?.d",
	"x? y! z?!",
	"user.admin?.to_s",
	"foo(:sym, key: v).bar",
	"é.end café",
	"$global.x @@cls.y",
	"a.b.",
	"a .",
	"x = y ? a : b",
	"items.each do |i| i.save end",
	"enddef if_x class_name do_this",
	"def end=(v); end",
	"self.class::CONST",
	"a\f.\rb",
	"a\v.b",
}

func checkRbChains(t *testing.T, line string) {
	t.Helper()
	var want [][2]int
	for _, m := range rbChainSpecRe.FindAllStringIndex(line, -1) {
		want = append(want, [2]int{m[0], m[1]})
	}
	if got := rbChains(line); !reflect.DeepEqual(got, want) {
		t.Fatalf("rbChains(%q) = %v, want %v", line, got, want)
	}
	for _, m := range want {
		if got, want := rbChainSegs(line[m[0]:m[1]]), rbSegSpecRe.Split(line[m[0]:m[1]], -1); !reflect.DeepEqual(got, want) {
			t.Fatalf("rbChainSegs(%q) = %q, want %q", line[m[0]:m[1]], got, want)
		}
	}
	want = nil
	for _, m := range rbKwSpecRe.FindAllStringIndex(line, -1) {
		want = append(want, [2]int{m[0], m[1]})
	}
	if got := rbKeywordSpans(line); !reflect.DeepEqual(got, want) {
		t.Fatalf("rbKeywordSpans(%q) = %v, want %v", line, got, want)
	}
}

func TestRbChainsMatchRegexp(t *testing.T) {
	for _, line := range rbChainSeeds {
		checkRbChains(t, line)
	}
}

func FuzzRbChains(f *testing.F) {
	for _, line := range rbChainSeeds {
		f.Add(line)
	}
	f.Fuzz(checkRbChains)
}

var stimDataHashSpecRe = regexp.MustCompile(`(?:\bdata\s*:|:data\s*=>|["']data["']\s*=>)\s*\{`)

var stimDataHashSeeds = []string{
	`<%= tag.div data: { controller: "x" } do %>`,
	`= link_to "x", y, :data => {action: "a"}`,
	`%div{"data" => { "cart-target": "t" }}`,
	`'data'=>{}`,
	`mydata: {} data:{} xdata => { :data=>{`,
	"data:\n  {",
	`:data: {`,
	`"data": { data: {`,
	`ddata: {`,
	`data`,
	`:data`,
	`"data"`,
	`data:data:{`,
	`:data => :data => {`,
}

func checkStimDataHashes(t *testing.T, src string) {
	t.Helper()
	var want [][2]int
	for _, m := range stimDataHashSpecRe.FindAllStringIndex(src, -1) {
		want = append(want, [2]int{m[0], m[1]})
	}
	if got := stimDataHashes(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("stimDataHashes(%q) = %v, want %v", src, got, want)
	}
}

func TestStimDataHashesMatchRegexp(t *testing.T) {
	for _, src := range stimDataHashSeeds {
		checkStimDataHashes(t, src)
	}
}

func FuzzStimDataHashes(f *testing.F) {
	for _, src := range stimDataHashSeeds {
		f.Add(src)
	}
	f.Fuzz(checkStimDataHashes)
}

var rbAssignSpecRe = regexp.MustCompile(`(?:^|[^\w.@$:])([a-z_]\w*)\s*(?:\|\||&&|[-+*/%])?=(?:[^=~>]|$)`)

var rbAssignSeeds = []string{
	"x = 1",
	"  x ||= y",
	"a = b = c",
	"a=b=c",
	"x == y",
	"x =~ /a/",
	"k => v",
	"@x = 1; y = 2",
	"self.x = 1",
	"$g = 1 :s = 2",
	"x +=1 y -= 2 z *= 3 w /= 4 m %= 5 n &&= 6",
	"x |= 1 y ^= 2 z ||  = 3",
	"x=",
	"é = 1 éx = 2 x =é y = 2",
	"x\t\f= 1",
	"x = \xff y = 1",
	"\xffx = 1",
	"a, b = c",
	"def x(a = 1, b = 2)",
	"if x = y",
	"Foo = 1 _x = 2 _ = 3",
	"x =\n",
}

func checkRbAssigns(t *testing.T, line string) {
	t.Helper()
	var want [][2]int
	for _, m := range rbAssignSpecRe.FindAllStringSubmatchIndex(line, -1) {
		want = append(want, [2]int{m[2], m[3]})
	}
	if got := rbAssigns(line); !reflect.DeepEqual(got, want) {
		t.Fatalf("rbAssigns(%q) = %v, want %v", line, got, want)
	}
}

func TestRbAssignsMatchRegexp(t *testing.T) {
	for _, line := range rbAssignSeeds {
		checkRbAssigns(t, line)
	}
}

func FuzzRbAssigns(f *testing.F) {
	for _, line := range rbAssignSeeds {
		f.Add(line)
	}
	f.Fuzz(checkRbAssigns)
}
