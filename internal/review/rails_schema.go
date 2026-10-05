package review

import (
	"regexp"
	"strings"
)

// Columns from db/schema.rb for the Ruby call graph: each column of a
// create_table is a synthetic definition "<column>title" owned by
// "<table>artworks", which the resolver puts on the table's model (by
// convention, or self.table_name) as an attribute: Artwork#title, answering
// the reader, writer, title? and the dirty-tracking methods. They live in
// schema.rb's scan (cached by its blob id like any file's), so a column
// removed in the change is a removed definition, and code still reading it
// is found.

var (
	rbSchemaTableRe = regexp.MustCompile(`^(\s*)create_table\s+["'](\w+)["']`)
	rbSchemaColRe   = regexp.MustCompile(`^\s*t\.(\w+)\s+["'](\w+)["']`)
	rbTableNameRe   = regexp.MustCompile(`(?m)^\s*self\.table_name\s*=\s*["'](\w+)["']`)
)

// rbIsSchema reports whether a Ruby file is a schema dump.
func rbIsSchema(src string) bool {
	return strings.Contains(src, "ActiveRecord::Schema") && strings.Contains(src, ".define(")
}

// rbSchemaScan reads schema.rb's columns as definitions.
func rbSchemaScan(raw []string) hFile {
	f := hFile{OK: true}
	table, indent := "", ""
	for i, line := range raw {
		if m := rbSchemaTableRe.FindStringSubmatch(line); m != nil {
			table, indent = m[2], m[1]
			continue
		}
		if table == "" {
			continue
		}
		if strings.TrimSpace(line) == "end" && strings.HasPrefix(line, indent+"end") {
			table = ""
			continue
		}
		m := rbSchemaColRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[2]
		switch m[1] {
		case "index", "check_constraint", "foreign_key", "exclusion_constraint", "unique_constraint":
			continue
		case "references", "belongs_to":
			name += "_id"
		}
		f.Defs = append(f.Defs, hDef{Name: "<column>" + name, Owner: "<table>" + table, Line: i + 1, End: i + 1,
			Body: hashOf(strings.Join(strings.Fields(line), " ")), Sig: hashOf("")})
	}
	return f
}

// rbTableNames reads the self.table_name overrides of a version's models:
// table → model, by the model's file (Zeitwerk).
func rbTableNames(idx *index) map[string]string {
	out := map[string]string{}
	isModel := func(p string) bool {
		rel := rbAppRel(p)
		return strings.HasPrefix(rel, "app/models/") && strings.HasSuffix(p, ".rb") && !strings.HasPrefix(rel, "app/models/concerns/")
	}
	idx.prefetchFor("rbtable.rb", isModel)
	for _, p := range idx.paths {
		if !isModel(p) {
			continue
		}
		t, _ := idx.symbols("rbtable.rb", p, func(src string) any {
			if m := rbTableNameRe.FindStringSubmatch(src); m != nil {
				return m[1]
			}
			return ""
		}).(string)
		if t != "" {
			out[t] = rbCamelize(strings.TrimSuffix(strings.TrimPrefix(rbAppRel(p), "app/models/"), ".rb"))
		}
	}
	return out
}

// tableModel is the model of a table: the one whose table_name names it,
// else Rails' convention (museum_locations → MuseumLocation).
func (l *rbCalls) tableModel(t string) string {
	if m := l.tables[t]; m != "" {
		return m
	}
	return rbCamelize(singularize(t))
}
