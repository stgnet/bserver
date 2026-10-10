package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// OrderedMap is a map that preserves insertion order of keys.
// It replaces map[string]interface{} for YAML data so that rendered
// HTML output (meta tags, links, CSS properties, etc.) always appears
// in the same order as the source YAML file.
type OrderedMap struct {
	keys []string
	m    map[string]interface{}
}

// NewOrderedMap creates an empty OrderedMap.
func NewOrderedMap() *OrderedMap {
	return &OrderedMap{m: make(map[string]interface{})}
}

// Get returns the value for key and whether it exists.
func (om *OrderedMap) Get(key string) (interface{}, bool) {
	v, ok := om.m[key]
	return v, ok
}

// Set adds or updates a key-value pair. New keys are appended to the end.
func (om *OrderedMap) Set(key string, value interface{}) {
	if _, exists := om.m[key]; !exists {
		om.keys = append(om.keys, key)
	}
	om.m[key] = value
}

// Delete removes a key.
func (om *OrderedMap) Delete(key string) {
	if _, exists := om.m[key]; !exists {
		return
	}
	delete(om.m, key)
	for i, k := range om.keys {
		if k == key {
			om.keys = append(om.keys[:i], om.keys[i+1:]...)
			break
		}
	}
}

// Has returns true if the key exists.
func (om *OrderedMap) Has(key string) bool {
	_, ok := om.m[key]
	return ok
}

// Keys returns the keys in insertion order.
func (om *OrderedMap) Keys() []string {
	return om.keys
}

// Len returns the number of entries.
func (om *OrderedMap) Len() int {
	return len(om.keys)
}

// Range iterates over entries in insertion order.
func (om *OrderedMap) Range(fn func(key string, value interface{}) bool) {
	for _, k := range om.keys {
		if !fn(k, om.m[k]) {
			break
		}
	}
}

// MarshalJSON implements json.Marshaler so that *OrderedMap serializes as a
// JSON object with keys in insertion order.
func (om *OrderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range om.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, err := json.Marshal(om.m[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// parseYAMLOrdered parses YAML data into interface{} values, using *OrderedMap
// for mappings instead of map[string]interface{} to preserve key order.
func parseYAMLOrdered(data []byte) (interface{}, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	if node.Kind == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	// yaml.Unmarshal wraps the content in a DocumentNode
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return yamlNodeToInterface(node.Content[0]), nil
	}
	return yamlNodeToInterface(&node), nil
}

// yamlNodeToInterface recursively converts a yaml.Node tree into Go values,
// using *OrderedMap for mapping nodes to preserve key order.
func yamlNodeToInterface(node *yaml.Node) interface{} {
	switch node.Kind {
	case yaml.ScalarNode:
		var val interface{}
		_ = node.Decode(&val)
		return val

	case yaml.MappingNode:
		om := NewOrderedMap()
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			val := yamlNodeToInterface(node.Content[i+1])
			om.Set(key, val)
		}
		return om

	case yaml.SequenceNode:
		list := make([]interface{}, len(node.Content))
		for i, child := range node.Content {
			list[i] = yamlNodeToInterface(child)
		}
		return list

	case yaml.AliasNode:
		if node.Alias != nil {
			return yamlNodeToInterface(node.Alias)
		}
		return nil

	default:
		return nil
	}
}
