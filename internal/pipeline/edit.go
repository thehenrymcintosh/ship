package pipeline

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SetYAML sets the value at a path of mapping keys in a YAML document,
// creating mappings on the way, and keeps the rest of the document,
// comments included, as far as the YAML library allows.
func SetYAML(data []byte, path []string, value any) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("empty path")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the document isn't a mapping")
	}
	var val yaml.Node
	if err := val.Encode(value); err != nil {
		return nil, err
	}
	n := doc.Content[0]
	for i, key := range path {
		last := i == len(path)-1
		var child *yaml.Node
		for j := 0; j+1 < len(n.Content); j += 2 {
			if n.Content[j].Value == key {
				child = n.Content[j+1]
				if last {
					// Keep the old value's style (a flow list stays one).
					val.Style |= child.Style & yaml.FlowStyle
					val.LineComment, val.HeadComment, val.FootComment = child.LineComment, child.HeadComment, child.FootComment
					n.Content[j+1] = &val
				}
				break
			}
		}
		if last {
			if child == nil {
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &val)
			}
			break
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		}
		if child.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s isn't a mapping", key)
		}
		n = child
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
