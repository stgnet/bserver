package main

import (
	"fmt"
	"strings"
)

// renderStyleYAML converts a style definition (map of selectors to properties)
// into CSS. Each rule is indented to depth, and single-property rules are
// collapsed onto one line when they fit within maxInlineTagLength.
func renderStyleYAML(val interface{}, depth int) string {
	m, ok := val.(*OrderedMap)
	if !ok {
		return ""
	}
	var sb strings.Builder
	m.Range(func(selector string, props interface{}) bool {
		propMap, ok := props.(*OrderedMap)
		if !ok {
			return true
		}
		// Try collapsing single-property rules onto one line
		// (skip when the value is a nested block like @media > selector)
		if propMap.Len() == 1 {
			var prop string
			var pval interface{}
			propMap.Range(func(k string, v interface{}) bool {
				prop = k
				pval = v
				return false
			})
			if _, nested := pval.(*OrderedMap); !nested {
				line := fmt.Sprintf("%s%s { %s: %v; }", indent(depth), selector, prop, pval)
				if len(line) <= maxInlineTagLength {
					sb.WriteString(line)
					sb.WriteByte('\n')
					return true
				}
			}
		}
		fmt.Fprintf(&sb, "%s%s {\n", indent(depth), selector)
		propMap.Range(func(prop string, pval interface{}) bool {
			if nested, ok := pval.(*OrderedMap); ok {
				// Nested CSS block (e.g., @media > selector)
				inner := NewOrderedMap()
				inner.Set(prop, nested)
				sb.WriteString(renderStyleYAML(inner, depth+1))
			} else {
				fmt.Fprintf(&sb, "%s%s: %v;\n", indent(depth+1), prop, pval)
			}
			return true
		})
		fmt.Fprintf(&sb, "%s}\n", indent(depth))
		return true
	})
	return sb.String()
}
