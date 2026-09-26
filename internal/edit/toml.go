package edit

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Whole TOML tables are treated as units: magpie owns the tables it writes
// (for example a model provider) and replaces them verbatim, while every
// other line of the file stays untouched.

// TOMLTables lists explicit ordinary table headers in file order, excluding
// array-table headers. A name is the header's key path: a quoted part keeps its
// quotes, and whitespace around the dots is not part of it, so `[ a . b ]` is
// the name `a.b`. Missing files return (nil, nil); errors include the path.
func TOMLTables(path string) ([]string, error) {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return nil, err
	}
	tables, err := parseTOMLTables(splitLines(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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
	raw, err := Read(path)
	if err != nil || raw == nil {
		return nil, err
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if table.from < 0 {
		return nil, nil
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
func SetTOMLTable(path, name string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	block := []string{"[" + name + "]"}
	for _, kv := range kvs {
		block = append(block, kv.Path+" = "+tomlLiteral(kv.Value))
	}
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
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
	return writeTOML(path, out)
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
	line := key + " = " + tomlLiteral(value)
	lines := splitLines(string(raw))
	table, err := parseTOMLTable(lines, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if table.from < 0 {
		out := trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, "["+name+"]", line, "")
		return writeTOML(path, out)
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
	out := append(append(append([]string{}, lines[:from]...), line), lines[to:]...)
	return writeTOML(path, out)
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

// DelTOMLTop removes top-level (pre-table) keys.
func DelTOMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	header := true
	for _, line := range splitLines(string(raw)) {
		if header && tomlTable.MatchString(line) {
			header = false
		}
		if header {
			if m := tomlKV.FindStringSubmatch(line); m != nil && drop[strings.Trim(m[1], `"`)] {
				continue
			}
		}
		out = append(out, line)
	}
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
	for _, table := range tables {
		if table.name == name {
			if table.array {
				return tomlTableSpan{}, fmt.Errorf("line %d, column %d: %q is an array table, expected an ordinary table", table.from+1, table.column, name)
			}
			return table, nil
		}
	}
	return tomlTableSpan{from: -1, to: -1}, nil
}

// parseTOMLTables finds both ordinary and array-table blocks in one parser pass.
// Only top-level expressions delimit edits, so lines inside multiline values
// cannot be mistaken for keys, comments, or table headers.
func parseTOMLTables(lines []string) ([]tomlTableSpan, error) {
	p := unstable.Parser{KeepComments: true}
	p.Reset([]byte(strings.Join(lines, "\n")))
	var tables []tomlTableSpan
	finishKey := func(to int) error {
		if len(tables) == 0 {
			return nil
		}
		table := &tables[len(tables)-1]
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
		shape := p.Shape(keyRange)
		i := shape.Start.Line - 1
		if err := finishKey(i); err != nil {
			return nil, err
		}
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			if len(tables) > 0 {
				tables[len(tables)-1].to = i
			}
			tables = append(tables, tomlTableSpan{
				from: i, to: len(lines), name: strings.Join(rawParts, "."),
				column: shape.Start.Column,
				array:  expr.Kind == unstable.ArrayTable,
			})
		case unstable.KeyValue:
			if len(tables) == 0 {
				continue
			}
			table := &tables[len(tables)-1]
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
				kv.comment = p.Shape(comment.Raw).Start.Column - 1
			}
			table.keys = append(table.keys, kv)
		}
	}
	if err := p.Error(); err != nil {
		return nil, tomlParseError(&p, err)
	}
	if err := finishKey(len(lines)); err != nil {
		return nil, err
	}
	return tables, nil
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
	data := []byte(joinLines(lines))
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

func tomlLiteral(v any) string {
	switch x := v.(type) {
	case Raw:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	}
	return strconv.Quote(toString(v))
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
