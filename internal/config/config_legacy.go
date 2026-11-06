package config

// YAML is confined to one-time legacy imports and the frozen deprecated
// /v0/management/config.yaml compatibility view. Native operations never use it.
import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

func decodeLegacyYAML(data []byte) (map[string]any, error) {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	normalizeLegacyNumbers(root)
	return root, nil
}

func normalizeLegacyNumbers(value any) any {
	switch v := value.(type) {
	case int:
		return int64(v)
	case map[string]any:
		for key, child := range v {
			v[key] = normalizeLegacyNumbers(child)
		}
	case []any:
		for i, child := range v {
			v[i] = normalizeLegacyNumbers(child)
		}
	default:
		return normalizeNativeDates(value)
	}
	return value
}

// YAMLToTOML imports a legacy document without modifying its source.
// Anchors are decoded once; nulls that cannot be represented without changing
// effective configuration cause an error instead of silently changing defaults.
func YAMLToTOML(data []byte) ([]byte, error) {
	root, err := decodeLegacyYAML(data)
	if err != nil {
		return nil, err
	}
	dropped, err := legacyNullPaths(root, "")
	if err != nil {
		return nil, err
	}
	before, err := decodeConfigMap(root)
	if err != nil {
		return nil, err
	}
	if err = NormalizeV8ConfigAliases(root); err != nil {
		return nil, err
	}
	for _, path := range v8Paths {
		if _, present := mapPath(root, path.current); present {
			if value, exists := mapPath(root, path.old); exists {
				if path.old == "api-keys" {
					if _, isMap := value.(map[string]any); isMap {
						continue
					}
				}
				deleteMapPath(root, path.old)
			}
		}
	}
	for _, family := range v8KeyFamilies {
		if _, present := mapPath(root, "api-keys."+family.current); present {
			delete(root, family.old)
		}
	}
	out, err := RenderConfigDocument(root)
	if err != nil {
		return nil, err
	}
	after, err := DecodeConfig(out, ConfigFormatTOML)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 && !equivalentConfigValues(reflect.ValueOf(before).Elem(), reflect.ValueOf(after).Elem()) {
		return nil, fmt.Errorf("toml: cannot express null for %s without changing config defaults; replace null with an explicit value", strings.Join(dropped, ", "))
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	layout := &tomlLayout{order: map[string][]string{}, known: map[string]map[string]bool{}, head: map[string][]string{}, line: map[string]string{}, foot: map[string][]string{}, arrays: map[string]int{}}
	if len(doc.Content) > 0 {
		collectLegacyLayout(layout, nil, doc.Content[0], map[*yaml.Node]bool{})
	}
	relocateConfigLayout(layout, root)
	w := nativeTOMLWriter{layout: layout}
	w.comments(layout.head[""])
	if err = w.table(nil, root, false); err != nil {
		return nil, err
	}
	w.comments(layout.foot[""])
	w.comments(layout.docFoot)
	return w.buf.Bytes(), nil
}

func collectLegacyLayout(layout *tomlLayout, path []string, node *yaml.Node, seen map[*yaml.Node]bool) {
	if node == nil || seen[node] {
		return
	}
	seen[node] = true
	defer delete(seen, node)
	if node.Kind == yaml.AliasNode {
		collectLegacyLayout(layout, path, node.Alias, seen)
		return
	}
	key := tomlPathKey(path)
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			k, v := node.Content[i], node.Content[i+1]
			if k.Tag == "!!merge" {
				continue
			}
			layout.add(path, k.Value)
			child := tomlChildPath(path, k.Value)
			p := tomlPathKey(child)
			if k.HeadComment != "" {
				layout.head[p] = strings.Split(k.HeadComment, "\n")
			}
			if k.LineComment != "" {
				layout.line[p] = k.LineComment
			} else {
				layout.line[p] = v.LineComment
			}
			if k.FootComment != "" {
				layout.foot[p] = strings.Split(k.FootComment, "\n")
			}
			collectLegacyLayout(layout, child, v, seen)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			collectLegacyLayout(layout, tomlChildPath(path, tomlIndexSegment(i)), child, seen)
		}
	}
	if node.HeadComment != "" {
		layout.head[key] = append(layout.head[key], strings.Split(node.HeadComment, "\n")...)
	}
	if node.FootComment != "" {
		layout.foot[key] = append(layout.foot[key], strings.Split(node.FootComment, "\n")...)
	}
}

func equivalentConfigValues(a, b reflect.Value) bool {
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// LegacyConfigYAMLView exists exclusively for the immutable deprecated
// GET /v0/management/config.yaml endpoint. Do not use it for storage or snapshots.
func LegacyConfigYAMLView(data []byte, format ConfigFormat) ([]byte, error) {
	if format == ConfigFormatYAML {
		return data, nil
	}
	root, err := ParseConfigDocument(data)
	if err != nil {
		return nil, err
	}
	layout, err := scanTOMLLayout(data)
	if err != nil {
		return nil, err
	}
	node, err := legacyYAMLViewNode(layout, nil, root)
	if err != nil {
		return nil, err
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}, FootComment: strings.Join(layout.docFoot, "\n")}
	var buf bytes.Buffer
	// Encoding is intentionally restricted to this frozen deprecated endpoint.
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err = encoder.Encode(doc); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err = encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// legacyYAMLViewNode is private to the frozen v0 YAML renderer; it is never used
// as a configuration document model by the native loader, writer or management.
func legacyYAMLViewNode(layout *tomlLayout, path []string, value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range layout.orderedKeys(path, v) {
			child := tomlChildPath(path, key)
			p := tomlPathKey(child)
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key, HeadComment: strings.Join(layout.head[p], "\n"), FootComment: strings.Join(layout.foot[p], "\n")}
			valueNode, err := legacyYAMLViewNode(layout, child, v[key])
			if err != nil {
				return nil, err
			}
			if valueNode.Kind == yaml.ScalarNode {
				valueNode.LineComment = layout.line[p]
			} else {
				keyNode.LineComment = layout.line[p]
			}
			node.Content = append(node.Content, keyNode, valueNode)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i, item := range v {
			child := tomlChildPath(path, tomlIndexSegment(i))
			p := tomlPathKey(child)
			element, err := legacyYAMLViewNode(layout, child, item)
			if err != nil {
				return nil, err
			}
			element.HeadComment = strings.Join(layout.head[p], "\n")
			element.LineComment = layout.line[p]
			element.FootComment = strings.Join(layout.foot[p], "\n")
			node.Content = append(node.Content, element)
		}
		return node, nil
	default:
		node := new(yaml.Node)
		if err := node.Encode(value); err != nil {
			return nil, err
		}
		return node, nil
	}
}

func legacyNullPaths(value any, path string) ([]string, error) {
	var dropped []string
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			nested := key
			if path != "" {
				nested = path + "." + key
			}
			if child == nil {
				dropped = append(dropped, nested)
				continue
			}
			paths, err := legacyNullPaths(child, nested)
			if err != nil {
				return nil, err
			}
			dropped = append(dropped, paths...)
		}
	case []any:
		for i, child := range v {
			nested := fmt.Sprintf("%s[%d]", path, i)
			if child == nil {
				return nil, fmt.Errorf("toml: cannot express null array element at %s", nested)
			}
			paths, err := legacyNullPaths(child, nested)
			if err != nil {
				return nil, err
			}
			dropped = append(dropped, paths...)
		}
	}
	return dropped, nil
}
