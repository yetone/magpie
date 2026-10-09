package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/yetone/magpie/internal/filememo"
)

// Whole TOML tables are treated as units: magpie owns the tables it writes
// (for example a model provider) and replaces them verbatim, while every
// other line of the file stays untouched.

// TOMLTables lists explicit ordinary table headers in file order, excluding
// array-table headers. A name is the header's key path: a quoted part keeps its
// quotes, and whitespace around the dots is not part of it, so `[ a . b ]` is
// the name `a.b`. Missing files return (nil, nil); errors include the path.
func TOMLTables(path string) ([]string, error) {
	tables, err := tomlTablesOf(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, table := range tables {
		if !table.array {
			out = append(out, table.name)
		}
	}
	return out, nil
}

// GetTOMLTable returns the scalar keys of one table, or (nil, nil) when
// the file or table is absent. Read and parse errors are returned with the path.
// Strings are decoded; other scalars (booleans, numbers, dates, and times)
// retain their literal text. Arrays and inline tables are omitted.
// An array table with the requested name is a type error, not an absent table.
func GetTOMLTable(path, name string) (map[string]string, error) {
	tables, err := tomlTablesOf(path)
	if err != nil {
		return nil, err
	}
	table, ok := tomlTableNamed(tables, name)
	if !ok {
		return nil, nil
	}
	if table.array {
		return nil, fmt.Errorf("%s: line %d, column %d: %q is an array table, expected an ordinary table", path, table.from+1, table.column, name)
	}
	out := map[string]string{}
	for _, kv := range table.keys {
		if kv.scalar {
			out[kv.name] = kv.value
		}
	}
	return out, nil
}

// SetTOMLTable replaces table `name` with the given keys, or appends it.
// An existing array table with that name is rejected.
//
// A key whose value is an Inline table is merged into the spelling the file
// already has for it, rather than written inline beside it, which would
// define the key twice: when the file holds it as its own table
// ([name.key], which a replacement of [name] leaves where it is), each entry
// is set in that table, its other keys and comments kept; when the table
// holds it as dotted keys (key."x" = …), those lines are kept as written and
// each entry is set among them. Otherwise it is written inline.
func SetTOMLTable(path, name string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	tables, err := parseTOMLTables(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	type subtable struct {
		name    string
		entries Inline
	}
	var subs []subtable
	block := []string{"[" + name + "]"}
	for _, kv := range kvs {
		if in, ok := kv.Value.(Inline); ok {
			if _, ok := tomlTableNamed(tables, name+"."+kv.Path); ok {
				subs = append(subs, subtable{name + "." + kv.Path, in})
				continue
			}
			if dotted := tomlDotted(lines, table, kv.Path, in); dotted != nil {
				block = append(block, dotted...)
				continue
			}
		}
		block = append(block, kv.Path+" = "+tomlLiteral(kv.Value))
	}
	from, to := table.from, table.to
	var out []string
	if from < 0 {
		out = trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
		out = append(out, "")
	} else {
		out = append(out, lines[:from]...)
		out = append(out, block...)
		if to < len(lines) && strings.TrimSpace(lines[to]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[to:]...)
	}
	for _, sub := range subs {
		for _, e := range sub.entries {
			if out, err = setTOMLKeyLines(out, sub.name, e.Path, tomlString(e.Path), e.Value); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	return writeTOML(path, out)
}

// tomlDotted is the lines of table's dotted keys under key (key."x" = …),
// each as written, with in's entries set among them: one already there
// replaced in place, the rest added after them. Nil when there are none.
func tomlDotted(lines []string, table tomlTableSpan, key string, in Inline) []string {
	var kept [][]string // each dotted key's lines
	at := map[string]int{}
	for _, kv := range table.keys {
		if strings.HasPrefix(kv.name, key+".") {
			at[kv.name] = len(kept)
			kept = append(kept, lines[kv.from:kv.to])
		}
	}
	if kept == nil {
		return nil
	}
	for _, e := range in {
		if i, ok := at[key+"."+e.Path]; ok {
			kv := tomlKeyNamed(table, key+"."+e.Path)
			kept[i] = []string{kv.prefix + tomlLiteral(e.Value) + kv.suffix}
			continue
		}
		kept = append(kept, []string{key + "." + tomlString(e.Path) + " = " + tomlLiteral(e.Value)})
	}
	var out []string
	for _, k := range kept {
		out = append(out, k...)
	}
	return out
}

func tomlKeyNamed(table tomlTableSpan, name string) tomlKeySpan {
	for _, kv := range table.keys {
		if kv.name == name {
			return kv
		}
	}
	return tomlKeySpan{}
}

// SetTOMLKey sets one key of table `name` and leaves the table's other
// lines as they are. Replacing a value preserves its surrounding whitespace
// and trailing comment. New keys go after the last complete value; missing
// tables are appended, while an array table with the same name is rejected.
func SetTOMLKey(path, name, key string, value any) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	out, err := setTOMLKeyLines(splitLines(string(raw)), name, key, key, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return writeTOML(path, out)
}

// setTOMLKeyLines is SetTOMLKey on lines: the key named `key` (as the parser
// names it, quotes taken off) is set, and a new one is written as `spelled`.
func setTOMLKeyLines(lines []string, name, key, spelled string, value any) ([]string, error) {
	line := spelled + " = " + tomlLiteral(value)
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return nil, err
	}
	if table.from < 0 {
		out := trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		return append(out, "["+name+"]", line, ""), nil
	}
	from, to := table.from+1, table.from+1
	for _, kv := range table.keys {
		if kv.name == key {
			from, to = kv.from, kv.to
			line = kv.prefix + tomlLiteral(value) + kv.suffix
			break
		}
		from, to = kv.to, kv.to
	}
	return append(append(append([]string{}, lines[:from]...), line), lines[to:]...), nil
}

// DelTOMLKey removes one key of table `name`, and the table with it when
// nothing else is left in it.
func DelTOMLKey(path, name, key string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if table.from < 0 {
		return nil
	}
	out := append([]string{}, lines[:table.from+1]...)
	at := table.from + 1
	for _, kv := range table.keys {
		if kv.name == key {
			out = append(out, lines[at:kv.from]...)
			at = kv.to
		}
	}
	out = append(out, lines[at:table.to]...)
	for _, line := range out[table.from+1:] {
		if strings.TrimSpace(line) != "" {
			out = append(out, lines[table.to:]...)
			return writeTOML(path, out)
		}
	}
	return DelTOMLTable(path, name)
}

// DelTOMLTable removes table `name` (header and body) if present.
// Child table and array-table blocks are outside that range and remain.
func DelTOMLTable(path, name string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	from, to := table.from, table.to
	if from < 0 {
		return nil
	}
	// Take the blank lines before the header with it so no gap is left.
	for from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	out := append([]string{}, lines[:from]...)
	if to < len(lines) && len(out) > 0 && strings.TrimSpace(lines[to]) != "" {
		out = append(out, "")
	}
	out = append(out, lines[to:]...)
	return writeTOML(path, out)
}

// GetTOMLTop reads a top-level (pre-table) key from a TOML file. A string is
// decoded; any other value is its text as written, without a trailing
// comment, an array or inline table over several lines included. A file
// TOML's parser refuses is read line by line, as well as that goes.
func GetTOMLTop(path, key string) (string, bool) {
	doc, err := tomlFileOf(path)
	if err != nil {
		return getTOMLTopLines(path, key)
	}
	for _, kv := range doc.root.keys {
		if kv.name == key {
			if kv.scalar {
				return kv.value, true
			}
			return kv.text, true
		}
	}
	return "", false
}

// getTOMLTopLines is GetTOMLTop for a file that isn't valid TOML: the first
// line that looks like `key = value` before one that looks like a header.
func getTOMLTopLines(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return "", false
	}
	for _, line := range splitLines(string(raw)) {
		if tomlTable.MatchString(line) {
			break
		}
		if m := tomlKV.FindStringSubmatch(line); m != nil && strings.Trim(m[1], `"`) == key {
			return tomlValue(m[2]), true
		}
	}
	return "", false
}

// SetTOMLTop sets top-level keys in a TOML file, as strings. An existing key
// is replaced as a whole, its value's every line with it; new keys go right
// after the last existing top-level key's value, or first in the file when
// there is none. Every key is located in the file as it was read, so the
// edit is one write that either happens entirely or not at all.
func SetTOMLTop(path string, kvs ...KV) error {
	return setTOMLTop(path, false, kvs...)
}

// SetTOMLTopPreserving is SetTOMLTop with an existing key's spacing and trailing
// comment kept. New keys use the same spelling as SetTOMLTop.
func SetTOMLTopPreserving(path string, kvs ...KV) error {
	return setTOMLTop(path, true, kvs...)
}

func setTOMLTop(path string, preserve bool, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	type span struct {
		to   int
		line string
	}
	replace := map[int]span{}
	var added []string
	addedAt := map[string]int{}
	at := 0
	if n := len(doc.root.keys); n > 0 {
		at = doc.root.keys[n-1].to
	}
	for _, kv := range kvs {
		line := kv.Path + " = " + tomlLiteral(kv.Value)
		found := false
		for _, k := range doc.root.keys {
			if k.name == kv.Path {
				if preserve {
					line = k.prefix + tomlLiteral(kv.Value) + k.suffix
				}
				replace[k.from] = span{k.to, line}
				found = true
				break
			}
		}
		switch i, ok := addedAt[kv.Path]; {
		case found:
		case ok:
			added[i] = line
		default:
			addedAt[kv.Path] = len(added)
			added = append(added, line)
		}
	}
	out := make([]string, 0, len(lines)+len(added))
	for i := 0; i < len(lines); {
		if i == at {
			out = append(out, added...)
			added = nil
		}
		if r, ok := replace[i]; ok {
			out = append(out, r.line)
			i = r.to
			continue
		}
		out = append(out, lines[i])
		i++
	}
	out = append(out, added...)
	return writeTOML(path, out)
}

// DelTOMLTop removes top-level (pre-table) keys, each with every line of its
// value.
func DelTOMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	at := 0
	for _, kv := range doc.root.keys {
		if drop[kv.name] {
			out = append(out, lines[at:kv.from]...)
			at = kv.to
		}
	}
	out = append(out, lines[at:]...)
	return writeTOML(path, out)
}

type tomlTableSpan struct {
	from, to int
	column   int // 1-based start of the table name.
	name     string
	array    bool
	keys     []tomlKeySpan
}

type tomlKeySpan struct {
	from, to       int // to is -1 until the next expression or EOF bounds the value.
	column         int // 1-based start of the key.
	name, value    string
	text           string // The value as written, from prefix to suffix.
	prefix, suffix string // Original text before and after the complete value.
	comment        int    // Column of a trailing comment, or -1 when absent.
	scalar         bool
}

// parseTOMLTable locates a table and its complete key/value expressions in
// the original lines. Ranges are [from, to); the table's from is -1 when absent.
// A child array table is a separate block, outside the target table's range;
// keeping it can implicitly recreate the parent after the parent is deleted.
func parseTOMLTable(lines []string, name string) (tomlTableSpan, error) {
	tables, err := parseTOMLTables(lines)
	if err != nil {
		return tomlTableSpan{}, err
	}
	if table, ok := tomlTableNamed(tables, name); ok {
		if table.array {
			return tomlTableSpan{}, fmt.Errorf("line %d, column %d: %q is an array table, expected an ordinary table", table.from+1, table.column, name)
		}
		return table, nil
	}
	return tomlTableSpan{from: -1, to: -1}, nil
}

func tomlTableNamed(tables []tomlTableSpan, name string) (tomlTableSpan, bool) {
	for _, table := range tables {
		if table.name == name {
			return table, true
		}
	}
	return tomlTableSpan{}, false
}

// tomlTablesOf is parseTOMLTables of a file, parsed again only once it
// changes: an agent's config can be large (Codex's lists every project it
// was trusted in) and is read field by field. Missing files are (nil, nil).
func tomlTablesOf(path string) ([]tomlTableSpan, error) {
	doc, err := tomlFileOf(path)
	return doc.tables, err
}

// tomlFileOf is parseTOMLFile of a file, kept as tomlTablesOf says. A missing
// file is an empty document and no error.
func tomlFileOf(path string) (tomlFile, error) {
	doc, err := filememo.Read("toml file", path, func(b []byte) (tomlFile, error) {
		doc, err := parseTOMLFile(splitLines(string(b)))
		if err != nil {
			return tomlFile{}, fmt.Errorf("%s: %w", path, err)
		}
		return doc, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return tomlFile{}, nil
	}
	return doc, err
}

// parseTOMLTables finds both ordinary and array-table blocks in one parser pass.
// Only top-level expressions delimit edits, so lines inside multiline values
// cannot be mistaken for keys, comments, or table headers.
func parseTOMLTables(lines []string) ([]tomlTableSpan, error) {
	doc, err := parseTOMLFile(lines)
	return doc.tables, err
}

// tomlFile is a file's top-level (pre-table) keys, as the unnamed table root
// spanning [0, first header), and its table blocks in file order.
type tomlFile struct {
	root   tomlTableSpan
	tables []tomlTableSpan
}

// parseTOMLFile is parseTOMLTables with the top-level keys too, located the
// same way: a key's range covers its whole value, however many lines it takes.
func parseTOMLFile(lines []string) (tomlFile, error) {
	p := unstable.Parser{KeepComments: true}
	p.Reset([]byte(strings.Join(lines, "\n")))
	shapeOf := tomlShaper(lines)
	root := tomlTableSpan{to: len(lines)}
	var tables []tomlTableSpan
	current := func() *tomlTableSpan {
		if len(tables) == 0 {
			return &root
		}
		return &tables[len(tables)-1]
	}
	finishKey := func(to int) error {
		table := current()
		if len(table.keys) == 0 {
			return nil
		}
		kv := &table.keys[len(table.keys)-1]
		if kv.to != -1 {
			return nil
		}
		// Array nodes have no complete Raw range. The next expression (including
		// standalone comments) bounds the value; only trailing blank lines remain.
		kv.to = to
		for kv.to > kv.from+1 && strings.TrimSpace(lines[kv.to-1]) == "" {
			kv.to--
		}
		if kv.to <= kv.from {
			return fmt.Errorf("line %d, column %d: cannot locate the end of the TOML value", kv.from+1, kv.column)
		}
		last := lines[kv.to-1]
		end := len(last)
		if kv.comment >= 0 {
			end = kv.comment
		}
		// Comments come from parser nodes, so '#' inside a string is part of
		// the value. Keep all whitespace between the closing value and comment.
		end = len(strings.TrimRight(last[:end], " \t\r"))
		kv.suffix = last[end:]
		text := strings.Join(lines[kv.from:kv.to], "\n")
		kv.text = text[len(kv.prefix) : len(text)-len(kv.suffix)]
		return nil
	}
	for p.NextExpression() {
		expr := p.Expression()
		keyRange := expr.Raw
		var parts, rawParts []string
		if expr.Kind != unstable.Comment {
			for keys := expr.Key(); keys.Next(); {
				key := keys.Node()
				if len(parts) == 0 {
					keyRange = key.Raw
				}
				parts = append(parts, string(key.Data))
				// A table's name is the parser's own key text, so a quoted part keeps
				// its quotes, while the whitespace a header may put around its dots,
				// as in `[ a . b ]`, is not part of the name.
				rawParts = append(rawParts, string(p.Raw(key.Raw)))
				keyRange.Length = key.Raw.Offset + key.Raw.Length - keyRange.Offset
			}
		}
		shape := shapeOf(keyRange)
		i := shape.Start.Line - 1
		if err := finishKey(i); err != nil {
			return tomlFile{}, err
		}
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			current().to = i
			tables = append(tables, tomlTableSpan{
				from: i, to: len(lines), name: strings.Join(rawParts, "."),
				column: shape.Start.Column,
				array:  expr.Kind == unstable.ArrayTable,
			})
		case unstable.KeyValue:
			table := current()
			value := expr.Value()
			// Start after the parsed key, which may itself contain '='.
			_, valueText, _ := strings.Cut(lines[i][shape.End.Column-1:], "=")
			start := len(lines[i]) - len(strings.TrimLeft(valueText, " \t"))
			kv := tomlKeySpan{
				from: i, to: -1, name: strings.Join(parts, "."),
				column: shape.Start.Column,
				prefix: lines[i][:start], comment: -1,
				value: string(value.Data), scalar: value.Kind != unstable.Array && value.Kind != unstable.InlineTable,
			}
			if comment := expr.Next(); comment != nil && comment.Kind == unstable.Comment {
				kv.comment = shapeOf(comment.Raw).Start.Column - 1
			}
			table.keys = append(table.keys, kv)
		}
	}
	if err := p.Error(); err != nil {
		return tomlFile{}, tomlParseError(&p, err)
	}
	if err := finishKey(len(lines)); err != nil {
		return tomlFile{}, err
	}
	return tomlFile{root: root, tables: tables}, nil
}

// tomlShaper is the parser's Shape for the document of lines, found from
// where each line starts rather than by counting the newlines before each
// range, as Shape does: that is a pass over the file for each key, which
// took a third of a second on a 1.5 MB config.toml of Codex's.
func tomlShaper(lines []string) func(unstable.Range) unstable.Shape {
	starts := make([]int, len(lines))
	for i, off := 1, 0; i < len(lines); i++ {
		off += len(lines[i-1]) + 1
		starts[i] = off
	}
	at := func(off int) unstable.Position {
		i := sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
		return unstable.Position{Offset: off, Line: i + 1, Column: off - starts[i] + 1}
	}
	return func(r unstable.Range) unstable.Shape {
		return unstable.Shape{Start: at(int(r.Offset)), End: at(int(r.Offset + r.Length))}
	}
}

func tomlParseError(p *unstable.Parser, err error) error {
	pe, ok := err.(*unstable.ParserError)
	if !ok {
		return err
	}
	data := p.Data()
	// Empty highlights are used for EOF errors. Do not pass them to Range:
	// the parser's slice-offset calculation can panic on a nil highlight.
	pos := unstable.Position{
		Offset: len(data), Line: bytes.Count(data, []byte{'\n'}) + 1,
		Column: len(data) - bytes.LastIndexByte(data, '\n'),
	}
	if len(pe.Highlight) > 0 {
		pos = p.Shape(p.Range(pe.Highlight)).Start
	}
	if pos.Offset == 0 && bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		err = fmt.Errorf("UTF-8 BOM is not supported; save the file without BOM: %w", err)
	}
	return fmt.Errorf("line %d, column %d: %w", pos.Line, pos.Column, err)
}

// writeTOML validates the exact bytes before any filesystem changes. Decoding
// also checks duplicate keys and conflicting definitions; it never rewrites
// the document, so comments and formatting survive validation unchanged.
func writeTOML(path string, lines []string) error {
	orig, _ := Read(path)
	data := []byte(joinLinesLike(lines, string(orig)))
	if err := validateTOML(data); err != nil {
		return fmt.Errorf("%s: edited TOML is invalid: %w", path, err)
	}
	return WriteAtomic(path, data)
}

func validateTOML(data []byte) error {
	// Check syntax first so EOF errors use the same nil-highlight-safe
	// diagnostics as reads, before the decoder checks TOML definitions.
	var p unstable.Parser
	p.Reset(data)
	for p.NextExpression() {
	}
	if err := p.Error(); err != nil {
		return tomlParseError(&p, err)
	}
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		if de, ok := err.(*toml.DecodeError); ok {
			line, column := de.Position()
			return fmt.Errorf("line %d, column %d: %w", line, column, err)
		}
		return err
	}
	return nil
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Raw is a TOML value written as it is: an array or an inline table the
// caller has already spelled out.
type Raw string

// Inline is an inline table, its keys in order, each written quoted. Given
// to SetTOMLTable it is merged into the spelling the file already has for
// the key (see there).
type Inline []KV

func tomlLiteral(v any) string {
	switch x := v.(type) {
	case Raw:
		return string(x)
	case Inline:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = tomlString(e.Path) + " = " + tomlLiteral(e.Value)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	}
	return tomlString(toString(v))
}

// tomlString spells s as a TOML basic string. Go's strconv.Quote is not a
// substitute: it escapes a control byte as \x01, and TOML defines no \x
// escape, so such a value leaves a file no TOML parser reads. writeTOML
// refuses that file before any change reaches the disk, so the config is
// never corrupted, but the edit then cannot be made at all — an agent's own
// value has to be writable. Escapes taken from the TOML spec; anything else
// that is printable, including non-ASCII, is written as it is.
func tomlString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			// One byte that is not part of a rune: TOML has no escape for a
			// single byte, only for code points, so a value that is not valid
			// UTF-8 cannot be written. It is left as it is, and writeTOML
			// refuses the file, rather than being quietly replaced by U+FFFD.
			b.WriteByte(s[i-size])
			continue
		}
		switch r {
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Table is one TOML table: its header name and its keys, in order.
type Table struct {
	Name string
	KVs  []KV
}

// SetTOMLTables replaces every ordinary or array table whose name begins with
// one of the prefixes by the given tables, appended at the end in one write: a set of
// tables magpie owns as a whole (one per model of its catalog), which would
// otherwise be rewritten once per table. With no tables it only removes.
func SetTOMLTables(path string, prefixes []string, tables []Table) error {
	return SetTOMLTablesMatching(path, nil, prefixes, tables)
}

// SetTOMLTablesMatching replaces tables whose names are exact matches or
// begin with one of prefixes, then appends the given tables in one write.
func SetTOMLTablesMatching(path string, names, prefixes []string, tables []Table) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	blocks, err := parseTOMLTables(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	owned := func(name string) bool {
		for _, n := range names {
			if name == n {
				return true
			}
		}
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	top := len(lines)
	if len(blocks) > 0 {
		top = blocks[0].from
	}
	out := append([]string{}, lines[:top]...)
	drop, found := false, false
	for _, block := range blocks {
		was := drop
		drop = owned(block.name)
		if drop {
			found = true
			out = trimBlank(out)
		} else if was && len(out) > 0 {
			out = append(out, "")
		}
		if !drop {
			out = append(out, lines[block.from:block.to]...)
		}
	}
	if !found && len(tables) == 0 {
		return nil
	}
	out = trimBlank(out)
	for _, t := range tables {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, "["+t.Name+"]")
		for _, kv := range t.KVs {
			out = append(out, kv.Path+" = "+tomlLiteral(kv.Value))
		}
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return writeTOML(path, out)
}
