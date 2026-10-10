package main

import (
	"encoding/json"
	"testing"
)

func TestOrderedMapBasicOperations(t *testing.T) {
	om := NewOrderedMap()

	// Empty map
	if om.Len() != 0 {
		t.Errorf("Len() = %d, want 0", om.Len())
	}
	if om.Has("x") {
		t.Error("Has('x') = true on empty map")
	}

	// Set and Get
	om.Set("b", 2)
	om.Set("a", 1)
	om.Set("c", 3)

	if om.Len() != 3 {
		t.Errorf("Len() = %d, want 3", om.Len())
	}

	v, ok := om.Get("a")
	if !ok || v != 1 {
		t.Errorf("Get('a') = %v, %v; want 1, true", v, ok)
	}

	_, ok = om.Get("missing")
	if ok {
		t.Error("Get('missing') should return false")
	}
}

func TestOrderedMapPreservesInsertionOrder(t *testing.T) {
	om := NewOrderedMap()
	om.Set("c", 3)
	om.Set("a", 1)
	om.Set("b", 2)

	keys := om.Keys()
	expected := []string{"c", "a", "b"}
	if len(keys) != len(expected) {
		t.Fatalf("Keys() length = %d, want %d", len(keys), len(expected))
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("Keys()[%d] = %q, want %q", i, k, expected[i])
		}
	}
}

func TestOrderedMapUpdateDoesNotReorder(t *testing.T) {
	om := NewOrderedMap()
	om.Set("first", 1)
	om.Set("second", 2)
	om.Set("first", 10) // update existing key

	keys := om.Keys()
	if len(keys) != 2 {
		t.Fatalf("Len() = %d after update, want 2", len(keys))
	}
	if keys[0] != "first" || keys[1] != "second" {
		t.Errorf("Keys() = %v, want [first second]", keys)
	}
	v, _ := om.Get("first")
	if v != 10 {
		t.Errorf("Get('first') = %v after update, want 10", v)
	}
}

func TestOrderedMapDelete(t *testing.T) {
	om := NewOrderedMap()
	om.Set("a", 1)
	om.Set("b", 2)
	om.Set("c", 3)

	om.Delete("b")

	if om.Len() != 2 {
		t.Errorf("Len() = %d after delete, want 2", om.Len())
	}
	if om.Has("b") {
		t.Error("Has('b') = true after delete")
	}
	keys := om.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "c" {
		t.Errorf("Keys() = %v after delete, want [a c]", keys)
	}

	// Deleting non-existent key is a no-op
	om.Delete("missing")
	if om.Len() != 2 {
		t.Errorf("Len() = %d after deleting missing key, want 2", om.Len())
	}
}

func TestOrderedMapRange(t *testing.T) {
	om := NewOrderedMap()
	om.Set("x", 10)
	om.Set("y", 20)
	om.Set("z", 30)

	var visited []string
	om.Range(func(k string, v interface{}) bool {
		visited = append(visited, k)
		return true
	})
	if len(visited) != 3 {
		t.Errorf("Range visited %d entries, want 3", len(visited))
	}

	// Early stop
	visited = nil
	om.Range(func(k string, v interface{}) bool {
		visited = append(visited, k)
		return k != "y" // stop after y
	})
	if len(visited) != 2 {
		t.Errorf("Range with early stop visited %d entries, want 2", len(visited))
	}
}

func TestOrderedMapMarshalJSON(t *testing.T) {
	om := NewOrderedMap()
	om.Set("name", "test")
	om.Set("value", 42)
	om.Set("active", true)

	data, err := json.Marshal(om)
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}

	expected := `{"name":"test","value":42,"active":true}`
	if string(data) != expected {
		t.Errorf("MarshalJSON = %s, want %s", data, expected)
	}
}

func TestOrderedMapMarshalJSONEmpty(t *testing.T) {
	om := NewOrderedMap()
	data, err := json.Marshal(om)
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("MarshalJSON empty = %s, want {}", data)
	}
}

func TestParseYAMLOrderedMapping(t *testing.T) {
	yaml := []byte("a: 1\nb: 2\nc: 3\n")
	result, err := parseYAMLOrdered(yaml)
	if err != nil {
		t.Fatalf("parseYAMLOrdered error: %v", err)
	}
	om, ok := result.(*OrderedMap)
	if !ok {
		t.Fatalf("expected *OrderedMap, got %T", result)
	}
	keys := om.Keys()
	expected := []string{"a", "b", "c"}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("key[%d] = %q, want %q", i, k, expected[i])
		}
	}
}

func TestParseYAMLOrderedSequence(t *testing.T) {
	yaml := []byte("- one\n- two\n- three\n")
	result, err := parseYAMLOrdered(yaml)
	if err != nil {
		t.Fatalf("parseYAMLOrdered error: %v", err)
	}
	list, ok := result.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", result)
	}
	if len(list) != 3 {
		t.Errorf("list length = %d, want 3", len(list))
	}
}

func TestParseYAMLOrderedNestedMap(t *testing.T) {
	yaml := []byte("outer:\n  inner: value\n")
	result, err := parseYAMLOrdered(yaml)
	if err != nil {
		t.Fatalf("parseYAMLOrdered error: %v", err)
	}
	om := result.(*OrderedMap)
	outerVal, ok := om.Get("outer")
	if !ok {
		t.Fatal("missing 'outer' key")
	}
	inner, ok := outerVal.(*OrderedMap)
	if !ok {
		t.Fatalf("expected nested *OrderedMap, got %T", outerVal)
	}
	v, ok := inner.Get("inner")
	if !ok || v != "value" {
		t.Errorf("inner.Get('inner') = %v, %v; want 'value', true", v, ok)
	}
}

func TestParseYAMLOrderedEmpty(t *testing.T) {
	_, err := parseYAMLOrdered([]byte(""))
	if err == nil {
		t.Error("expected error for empty YAML, got nil")
	}
}

func TestParseYAMLOrderedInvalid(t *testing.T) {
	_, err := parseYAMLOrdered([]byte(":\n  : :\n\t\t"))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}
