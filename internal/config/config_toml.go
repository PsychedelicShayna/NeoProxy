package config

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ParseConfigDocument decodes a native TOML document without a YAML intermediate.
func ParseConfigDocument(data []byte) (map[string]any, error) {
	root := make(map[string]any)
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, tomlError(err)
	}
	normalizeNativeDates(root)
	return root, nil
}

func normalizeNativeDates(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = normalizeNativeDates(child)
		}
	case []any:
		for i, child := range v {
			v[i] = normalizeNativeDates(child)
		}
	case time.Time:
		return v.Format(time.RFC3339Nano)
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return fmt.Sprint(v)
	}
	return value
}

// RenderConfigDocument renders a native TOML map.
func RenderConfigDocument(root map[string]any) ([]byte, error) { return MarshalTOML(root) }

// RenderConfigDocumentPreserve renders native TOML while retaining comments and
// key order from source, including metadata on relocated legacy fields.
func RenderConfigDocumentPreserve(root map[string]any, source []byte) ([]byte, error) {
	return renderNativePreserve(root, source)
}

// MarshalTOML serializes configuration values with their canonical kebab-case keys.
func MarshalTOML(value any) ([]byte, error) {
	native, err := nativeConfigValue(reflect.ValueOf(value))
	if err != nil {
		return nil, err
	}
	return toml.Marshal(native)
}

func nativeConfigValue(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if v.Type() == reflect.TypeOf(time.Duration(0)) {
		return time.Duration(v.Int()).String(), nil
	}
	if v.Type() == imageGenerationModeType {
		mode := v.Interface().(DisableImageGenerationMode)
		switch mode {
		case DisableImageGenerationAll:
			return true, nil
		case DisableImageGenerationChat, DisableImageGenerationPassthrough:
			return mode.String(), nil
		default:
			return false, nil
		}
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return v.Interface().(time.Time).Format(time.RFC3339Nano), nil
	}
	if v.Type() == pluginInstanceType {
		raw := v.Interface().(PluginInstanceConfig).Raw
		if raw == nil {
			return map[string]any{}, nil
		}
		return nativeConfigValue(reflect.ValueOf(raw))
	}
	if v.Type() == concurrencyType {
		return nativeConcurrencyValue(v.Interface().(CredentialConcurrencyConfig)), nil
	}
	switch v.Kind() {
	case reflect.Struct:
		root := make(map[string]any)
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := field.Tag.Get("toml")
			if tag == "" {
				tag = field.Tag.Get("yaml")
			}
			parts := strings.Split(tag, ",")
			key := parts[0]
			if key == "-" {
				continue
			}
			if strings.Contains(tag, ",omitempty") && v.Field(i).IsZero() {
				continue
			}
			child, err := nativeConfigValue(v.Field(i))
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			if field.Anonymous {
				if m, ok := child.(map[string]any); ok {
					for k, val := range m {
						root[k] = val
					}
				}
				continue
			}
			if key == "" {
				key = strings.ToLower(field.Name)
			}
			root[key] = child
		}
		return root, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		root := make(map[string]any, v.Len())
		it := v.MapRange()
		for it.Next() {
			child, err := nativeConfigValue(it.Value())
			if err != nil {
				return nil, err
			}
			if child != nil {
				root[fmt.Sprint(it.Key().Interface())] = child
			}
		}
		return root, nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		out := make([]any, v.Len())
		for i := range out {
			child, err := nativeConfigValue(v.Index(i))
			if err != nil {
				return nil, err
			}
			if child == nil {
				return nil, fmt.Errorf("toml: cannot express null array element at index %d", i)
			}
			out[i] = child
		}
		return out, nil
	default:
		return v.Interface(), nil
	}
}

// renderNativePreserve keeps native TOML comments and the order of existing keys.
func renderNativePreserve(root map[string]any, source []byte) ([]byte, error) {
	layout, err := configLayoutForRoot(source, root)
	if err != nil {
		return nil, tomlError(err)
	}
	w := nativeTOMLWriter{layout: layout}
	if err = w.table(nil, root, false); err != nil {
		return nil, err
	}
	w.comments(layout.docFoot)
	return w.buf.Bytes(), nil
}

type nativeTOMLWriter struct {
	buf    bytes.Buffer
	layout *tomlLayout
}

func (w *nativeTOMLWriter) comments(lines []string) {
	for _, line := range lines {
		if line != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			line = "# " + line
		}
		w.buf.WriteString(line)
		w.buf.WriteByte('\n')
	}
}
func nativeTOMLKey(key string) string {
	if key != "" {
		bare := true
		for _, r := range key {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				bare = false
				break
			}
		}
		if bare {
			return key
		}
	}
	encoded, err := toml.Marshal(map[string]any{key: true})
	if err == nil {
		if text, _, ok := strings.Cut(string(encoded), " = "); ok {
			return text
		}
	}
	return strconv.Quote(key)
}
func nativeTOMLPath(path []string) string {
	var parts []string
	for _, key := range path {
		if !strings.HasPrefix(key, "#") {
			parts = append(parts, nativeTOMLKey(key))
		}
	}
	return strings.Join(parts, ".")
}
func nativeTableArray(value any) ([]any, bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return nil, false
		}
	}
	return items, true
}
func (w *nativeTOMLWriter) table(path []string, root map[string]any, header bool) error {
	if header {
		w.buf.WriteByte('\n')
		w.comments(w.layout.head[tomlPathKey(path)])
		w.buf.WriteString("[" + nativeTOMLPath(path) + "]")
		if line := w.layout.line[tomlPathKey(path)]; line != "" {
			w.buf.WriteByte(' ')
			w.buf.WriteString(line)
		}
		w.buf.WriteByte('\n')
	}
	keys := w.layout.orderedKeys(path, root)
	for _, key := range keys {
		if _, ok := root[key].(map[string]any); ok {
			continue
		}
		if _, ok := nativeTableArray(root[key]); ok {
			continue
		}
		if root[key] == nil {
			// TOML omits cleared values, but their comments still belong to the
			// document. Emit them detached so a later save cannot discard them.
			p := tomlPathKey(tomlChildPath(path, key))
			w.comments(w.layout.head[p])
			if line := w.layout.line[p]; line != "" {
				w.comments([]string{line})
			}
			w.comments(w.layout.foot[p])
			continue
		}
		child := tomlChildPath(path, key)
		p := tomlPathKey(child)
		w.comments(w.layout.head[p])
		w.buf.WriteString(nativeTOMLKey(key) + " = ")
		if err := w.value(child, root[key]); err != nil {
			return err
		}
		if line := w.layout.line[p]; line != "" {
			w.buf.WriteByte(' ')
			w.buf.WriteString(line)
		}
		w.buf.WriteByte('\n')
		w.comments(w.layout.foot[p])
	}
	for _, key := range keys {
		childPath := tomlChildPath(path, key)
		if child, ok := root[key].(map[string]any); ok {
			if err := w.table(childPath, child, true); err != nil {
				return err
			}
			continue
		}
		if items, ok := nativeTableArray(root[key]); ok {
			w.comments(w.layout.head[tomlPathKey(childPath)])
			for i, raw := range items {
				itemPath := tomlChildPath(childPath, tomlIndexSegment(i))
				w.buf.WriteByte('\n')
				w.comments(w.layout.head[tomlPathKey(itemPath)])
				w.buf.WriteString("[[" + nativeTOMLPath(childPath) + "]]")
				if line := w.layout.line[tomlPathKey(itemPath)]; line != "" {
					w.buf.WriteByte(' ')
					w.buf.WriteString(line)
				}
				w.buf.WriteByte('\n')
				if err := w.table(itemPath, raw.(map[string]any), false); err != nil {
					return err
				}
				w.comments(w.layout.foot[tomlPathKey(itemPath)])
			}
			w.comments(w.layout.foot[tomlPathKey(childPath)])
		}
	}
	if header {
		w.comments(w.layout.foot[tomlPathKey(path)])
	}
	return nil
}

func (w *nativeTOMLWriter) value(path []string, value any) error {
	if items, ok := value.([]any); ok {
		multiline := false
		for i := range items {
			p := tomlPathKey(tomlChildPath(path, tomlIndexSegment(i)))
			if len(w.layout.head[p]) > 0 || w.layout.line[p] != "" || len(w.layout.foot[p]) > 0 {
				multiline = true
				break
			}
		}
		if multiline {
			w.buf.WriteString("[\n")
			for i, item := range items {
				child := tomlChildPath(path, tomlIndexSegment(i))
				p := tomlPathKey(child)
				w.comments(w.layout.head[p])
				w.buf.WriteString("  ")
				if err := w.value(child, item); err != nil {
					return err
				}
				w.buf.WriteByte(',')
				if line := w.layout.line[p]; line != "" {
					w.buf.WriteByte(' ')
					w.buf.WriteString(line)
				}
				w.buf.WriteByte('\n')
				w.comments(w.layout.foot[p])
			}
			w.buf.WriteByte(']')
			return nil
		}
	}
	text, err := nativeTOMLInline(value)
	if err != nil {
		return err
	}
	w.buf.WriteString(text)
	return nil
}
func nativeTOMLInline(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", fmt.Errorf("toml: cannot express null")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var parts []string
		for _, key := range keys {
			if v[key] == nil {
				continue
			}
			text, err := nativeTOMLInline(v[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, nativeTOMLKey(key)+" = "+text)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case []any:
		parts := make([]string, len(v))
		for i, child := range v {
			text, err := nativeTOMLInline(child)
			if err != nil {
				return "", err
			}
			parts[i] = text
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		encoded, err := toml.Marshal(map[string]any{"value": value})
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(string(encoded))
		_, text, ok := strings.Cut(text, " = ")
		if !ok {
			return "", fmt.Errorf("toml: unsupported scalar %T", value)
		}
		return text, nil
	}
}

// tomlError adds the line and column of a TOML syntax error to its message.
func tomlError(err error) error {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		row, column := decodeErr.Position()
		return fmt.Errorf("toml: line %d column %d: %s", row, column, decodeErr.Error())
	}
	var parserErr *unstable.ParserError
	if errors.As(err, &parserErr) {
		return fmt.Errorf("toml: %s", parserErr.Error())
	}
	return err
}

// tomlLayout records what toml.Unmarshal discards: the order keys appear in and
// the comments attached to them. Paths are joined key segments; array elements
// use "#<index>" segments.
type tomlLayout struct {
	order   map[string][]string
	known   map[string]map[string]bool
	head    map[string][]string
	line    map[string]string
	foot    map[string][]string
	arrays  map[string]int
	docFoot []string
}

const tomlPathSeparator = "\x1f"

func tomlPathKey(path []string) string { return strings.Join(path, tomlPathSeparator) }

func tomlChildPath(path []string, segment string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = segment
	return out
}

func tomlIndexSegment(index int) string { return "#" + strconv.Itoa(index) }

// tomlComment is a standalone comment line and whether an empty line borders it.
type tomlComment struct {
	text        string
	blankBefore bool
	blankAfter  bool
}

func scanTOMLLayout(data []byte) (*tomlLayout, error) {
	l := &tomlLayout{
		order:  map[string][]string{},
		known:  map[string]map[string]bool{},
		head:   map[string][]string{},
		line:   map[string]string{},
		foot:   map[string][]string{},
		arrays: map[string]int{},
	}
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(data)
	var current, previous []string
	var pending []tomlComment
	// attach assigns the comments gathered since the previous item the way YAML
	// does: a block directly below the previous item and followed by an empty
	// line is its foot comment; the rest heads the next item, with empty lines
	// between blocks kept.
	attach := func() []string {
		rest := pending
		pending = nil
		if previous != nil && len(rest) > 0 && !rest[0].blankBefore {
			end := 0
			for end < len(rest) && !rest[end].blankAfter {
				end++
			}
			if end < len(rest) {
				previousKey := tomlPathKey(previous)
				for _, c := range rest[:end+1] {
					l.foot[previousKey] = append(l.foot[previousKey], c.text)
				}
				rest = rest[end+1:]
			}
		}
		var head []string
		if previous != nil && len(rest) > 0 && rest[0].blankBefore {
			// A leading empty entry makes the YAML encoder keep the empty line.
			head = append(head, "")
		}
		for i, c := range rest {
			head = append(head, c.text)
			if c.blankAfter && i < len(rest)-1 {
				head = append(head, "")
			}
		}
		return head
	}
	for parser.NextExpression() {
		expr := parser.Expression()
		switch expr.Kind {
		case unstable.Comment:
			start := int(expr.Raw.Offset)
			pending = append(pending, tomlComment{
				text:        tomlCommentText(expr.Data),
				blankBefore: tomlBlankLineBefore(data, start),
				blankAfter:  tomlBlankLineAfter(data, start+int(expr.Raw.Length)),
			})
		case unstable.KeyValue:
			keys := tomlKeyParts(expr.Key())
			path := current
			for _, key := range keys {
				l.add(path, key)
				path = tomlChildPath(path, key)
			}
			l.comment(path, attach(), tomlTrailingComment(expr))
			l.scanValue(path, expr.Value())
			previous = path
		case unstable.Table, unstable.ArrayTable:
			head := attach()
			current = l.resolveTable(tomlKeyParts(expr.Key()), expr.Kind == unstable.ArrayTable)
			l.comment(current, head, tomlTrailingComment(expr))
			previous = current
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	for _, c := range pending {
		l.docFoot = append(l.docFoot, c.text)
	}
	return l, nil
}

// tomlBlankLineBefore reports whether the line above the one holding offset is
// empty or offset is on the first line.
func tomlBlankLineBefore(data []byte, offset int) bool {
	lineStart := bytes.LastIndexByte(data[:offset], '\n')
	if lineStart < 0 {
		return true
	}
	previousStart := bytes.LastIndexByte(data[:lineStart], '\n') + 1
	return len(bytes.TrimSpace(data[previousStart:lineStart])) == 0
}

// tomlBlankLineAfter reports whether the line below the one ending at offset is
// empty or absent.
func tomlBlankLineAfter(data []byte, offset int) bool {
	lineEnd := bytes.IndexByte(data[offset:], '\n')
	if lineEnd < 0 {
		return true
	}
	next := data[offset+lineEnd+1:]
	if nextEnd := bytes.IndexByte(next, '\n'); nextEnd >= 0 {
		next = next[:nextEnd]
	}
	return len(bytes.TrimSpace(next)) == 0
}

// resolveTable returns the path a table header opens. A segment naming an array
// of tables refers to its latest element; an [[array]] header appends one.
func (l *tomlLayout) resolveTable(keys []string, arrayTable bool) []string {
	var path []string
	for i, key := range keys {
		l.add(path, key)
		path = tomlChildPath(path, key)
		pathKey := tomlPathKey(path)
		if arrayTable && i == len(keys)-1 {
			index := l.arrays[pathKey]
			l.arrays[pathKey] = index + 1
			path = tomlChildPath(path, tomlIndexSegment(index))
			continue
		}
		if count, ok := l.arrays[pathKey]; ok && count > 0 {
			path = tomlChildPath(path, tomlIndexSegment(count-1))
		}
	}
	return path
}

func (l *tomlLayout) scanValue(path []string, value *unstable.Node) {
	switch value.Kind {
	case unstable.InlineTable:
		children := value.Children()
		for children.Next() {
			kv := children.Node()
			if kv.Kind != unstable.KeyValue {
				continue
			}
			child := path
			for _, key := range tomlKeyParts(kv.Key()) {
				l.add(child, key)
				child = tomlChildPath(child, key)
			}
			l.scanValue(child, kv.Value())
		}
	case unstable.Array:
		index := 0
		var pending []string
		children := value.Children()
		for children.Next() {
			element := children.Node()
			if element.Kind == unstable.Comment {
				pending = append(pending, tomlCommentText(element.Data))
				for nested := element.Child(); nested != nil; nested = nested.Next() {
					pending = append(pending, tomlCommentText(nested.Data))
				}
				continue
			}
			elementPath := tomlChildPath(path, tomlIndexSegment(index))
			l.comment(elementPath, pending, "")
			pending = nil
			l.scanValue(elementPath, element)
			index++
		}
	}
}

func (l *tomlLayout) add(path []string, key string) {
	pathKey := tomlPathKey(path)
	seen := l.known[pathKey]
	if seen == nil {
		seen = map[string]bool{}
		l.known[pathKey] = seen
	}
	if !seen[key] {
		seen[key] = true
		l.order[pathKey] = append(l.order[pathKey], key)
	}
}

func (l *tomlLayout) comment(path []string, head []string, line string) {
	pathKey := tomlPathKey(path)
	if len(head) > 0 {
		l.head[pathKey] = append(l.head[pathKey], head...)
	}
	if line != "" {
		l.line[pathKey] = line
	}
}

func tomlKeyParts(it unstable.Iterator) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}

func tomlTrailingComment(expr *unstable.Node) string {
	if next := expr.Next(); next != nil && next.Kind == unstable.Comment {
		return tomlCommentText(next.Data)
	}
	return ""
}

func tomlCommentText(data []byte) string {
	return strings.TrimRight(string(data), " \t\r")
}

// orderedKeys returns the keys of m in document order. Keys the layout scan did
// not see, which a valid document cannot produce, follow in sorted order.
func (l *tomlLayout) orderedKeys(path []string, m map[string]any) []string {
	keys := make([]string, 0, len(m))
	listed := make(map[string]bool, len(m))
	for _, key := range l.order[tomlPathKey(path)] {
		if _, ok := m[key]; ok && !listed[key] {
			keys = append(keys, key)
			listed[key] = true
		}
	}
	var rest []string
	for key := range m {
		if !listed[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// configLayoutForRoot relocates metadata when legacy fields move to canonical
// tables. Comments on pruned ancestors travel with the first surviving field.
func configLayoutForRoot(source []byte, root map[string]any) (*tomlLayout, error) {
	layout, err := scanTOMLLayout(source)
	if err != nil {
		return nil, err
	}
	relocateConfigLayout(layout, root)
	return layout, nil
}

func relocateConfigLayout(layout *tomlLayout, root map[string]any) {
	paths := append(append(append(append([]configPath{}, v8Aliases...), v8Paths...), v8StructPaths...), v8SharedStructPaths...)
	for _, family := range v8KeyFamilies {
		paths = append(paths, configPath{family.old, "api-keys." + family.current})
	}
	for _, entry := range paths {
		if _, exists := mapPath(root, entry.old); exists {
			continue
		}
		if _, exists := mapPath(root, entry.current); !exists {
			continue
		}
		from := tomlPathKey(strings.Split(entry.old, "."))
		to := tomlPathKey(strings.Split(entry.current, "."))
		for key, comments := range layout.head {
			if key == from || strings.HasPrefix(key, from+tomlPathSeparator) {
				dest := to + strings.TrimPrefix(key, from)
				layout.head[dest] = append(layout.head[dest], comments...)
				delete(layout.head, key)
			}
		}
		for key, comments := range layout.foot {
			if key == from || strings.HasPrefix(key, from+tomlPathSeparator) {
				dest := to + strings.TrimPrefix(key, from)
				layout.foot[dest] = append(layout.foot[dest], comments...)
				delete(layout.foot, key)
			}
		}
		for key, comment := range layout.line {
			if key == from || strings.HasPrefix(key, from+tomlPathSeparator) {
				dest := to + strings.TrimPrefix(key, from)
				if layout.line[dest] == "" {
					layout.line[dest] = comment
				} else if comment != "" {
					layout.head[dest] = append(layout.head[dest], comment)
				}
				delete(layout.line, key)
			}
		}
		oldParts := strings.Split(entry.old, ".")
		for end := len(oldParts) - 1; end > 0; end-- {
			ancestor := strings.Join(oldParts[:end], ".")
			if _, exists := mapPath(root, ancestor); exists {
				break
			}
			key := tomlPathKey(oldParts[:end])
			layout.head[to] = append(layout.head[to], layout.head[key]...)
			if comment := layout.line[key]; comment != "" {
				layout.head[to] = append(layout.head[to], comment)
			}
			layout.foot[to] = append(layout.foot[to], layout.foot[key]...)
			delete(layout.head, key)
			delete(layout.line, key)
			delete(layout.foot, key)
		}
	}
}
