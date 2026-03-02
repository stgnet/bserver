package main

import (
	"fmt"
	"html"
	"strings"
)

// formatDef describes how a name should be rendered as HTML (from ^name keys).
type formatDef struct {
	Tag               string       // HTML tag to use
	Params            *OrderedMap  // HTML attributes (may contain $key, $value, $varname); values are strings
	ParamsWildcard    bool         // if true, params: '$*' means use content entries as attributes
	Contents          string       // how to render inner content ("$*" = as-is, "" = iterate)
	ContentWrap       interface{}  // structured content wrapper (e.g., {card-body: '$*'})
	ContentWrapPlural bool         // true when "contents:" (plural) was used: wrap each iterable individually
	Script            string       // script language: "python", "javascript", "php", "sh"
	Code              string       // inline script code (per-record body)
	File              string       // script file to load code from (relative to docRoot)
	Markup            string       // markup language for content: "markdown"
	Sequence          []*formatDef // array format: multiple tags rendered in sequence
	Layout            string       // layout primitive: flex, grid, stack
	Gap               string       // layout gap (e.g. "1rem")
	Columns           string       // grid template columns
	Align             string       // align-items value
	Justify           string       // justify-content value
	Wrap              bool         // flex-wrap: wrap
	Variants          *OrderedMap  // variants map: name -> params map
	Slots             *OrderedMap  // slots map: slotName -> default content
	Defaults          *OrderedMap  // default vars: key -> value
	Required          []string     // required vars/props
}

// parseFormatDef parses a ^name value into a formatDef struct.
// If the value is a YAML array, each element is parsed as a separate formatDef
// and stored in the Sequence field. When rendered, each element produces its
// own tag in order, all receiving the same content/variables.
func parseFormatDef(v interface{}) *formatDef {
	// Handle array of format defs: each element is rendered in sequence
	if arr, ok := v.([]interface{}); ok {
		var seq []*formatDef
		for _, item := range arr {
			if _, isMap := item.(*OrderedMap); isMap {
				seq = append(seq, parseFormatDef(item))
			}
		}
		if len(seq) == 0 {
			return &formatDef{}
		}
		return &formatDef{Sequence: seq}
	}
	m, ok := v.(*OrderedMap)
	if !ok {
		return &formatDef{}
	}
	fd := &formatDef{}
	if tagVal, ok := m.Get("tag"); ok {
		if tag, ok := tagVal.(string); ok {
			fd.Tag = tag
		}
	}
	// Accept both "contents" (plural) and "content" (singular).
	// For string values, they behave identically (set fd.Contents).
	// For non-string values (structural wrappers like {li: '$*'}):
	//   "contents:" (plural) = wrap each iterable item individually
	//   "content:"  (singular) = wrap all items as a single block
	contentVal, hasContent := m.Get("contents")
	isPlural := hasContent
	if !hasContent {
		contentVal, hasContent = m.Get("content")
	}
	if hasContent {
		if s, ok := contentVal.(string); ok {
			fd.Contents = s
			fd.ContentWrapPlural = isPlural
		} else if contentVal != nil {
			// Non-string content (e.g., {card-body: '$*'}) is a structural wrapper
			fd.ContentWrap = contentVal
			fd.ContentWrapPlural = isPlural
		}
	}
	if paramsVal, ok := m.Get("params"); ok {
		if paramsStr, ok := paramsVal.(string); ok && paramsStr == "$*" {
			fd.ParamsWildcard = true
		} else if params, ok := paramsVal.(*OrderedMap); ok {
			fd.Params = NewOrderedMap()
			params.Range(func(pk string, pv interface{}) bool {
				fd.Params.Set(pk, fmt.Sprintf("%v", pv))
				return true
			})
		}
	}
	if scriptVal, ok := m.Get("script"); ok {
		if script, ok := scriptVal.(string); ok {
			fd.Script = script
		}
	}
	if codeVal, ok := m.Get("code"); ok {
		if code, ok := codeVal.(string); ok {
			fd.Code = code
		}
	}
	if fileVal, ok := m.Get("file"); ok {
		if file, ok := fileVal.(string); ok {
			fd.File = file
		}
	}
	if markupVal, ok := m.Get("markup"); ok {
		if markup, ok := markupVal.(string); ok {
			fd.Markup = markup
		}
	}
	if layoutVal, ok := m.Get("layout"); ok {
		if layout, ok := layoutVal.(string); ok {
			fd.Layout = strings.ToLower(layout)
		}
	}
	if gapVal, ok := m.Get("gap"); ok {
		fd.Gap = fmt.Sprintf("%v", gapVal)
	}
	if colsVal, ok := m.Get("columns"); ok {
		fd.Columns = fmt.Sprintf("%v", colsVal)
	}
	if alignVal, ok := m.Get("align"); ok {
		fd.Align = fmt.Sprintf("%v", alignVal)
	}
	if justifyVal, ok := m.Get("justify"); ok {
		fd.Justify = fmt.Sprintf("%v", justifyVal)
	}
	if wrapVal, ok := m.Get("wrap"); ok {
		if b, ok := wrapVal.(bool); ok {
			fd.Wrap = b
		}
	}
	if variantsVal, ok := m.Get("variants"); ok {
		if variants, ok := variantsVal.(*OrderedMap); ok {
			fd.Variants = variants
		}
	}
	if slotsVal, ok := m.Get("slots"); ok {
		if slots, ok := slotsVal.(*OrderedMap); ok {
			fd.Slots = slots
		}
	}
	if defaultsVal, ok := m.Get("defaults"); ok {
		if defaults, ok := defaultsVal.(*OrderedMap); ok {
			fd.Defaults = defaults
		}
	}
	if requiredVal, ok := m.Get("required"); ok {
		if arr, ok := requiredVal.([]interface{}); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok && s != "" {
					fd.Required = append(fd.Required, s)
				}
			}
		}
	}
	return fd
}

func mergedFormatParams(fd *formatDef, vars map[string]string) *OrderedMap {
	if fd == nil {
		return nil
	}
	out := NewOrderedMap()
	if fd.Params != nil {
		fd.Params.Range(func(k string, v interface{}) bool {
			out.Set(k, v)
			return true
		})
	}
	if fd.Variants != nil && vars != nil {
		if variant, ok := vars["variant"]; ok && variant != "" {
			if vv, ok := fd.Variants.Get(variant); ok {
				if vm, ok := vv.(*OrderedMap); ok {
					vm.Range(func(k string, v interface{}) bool {
						out.Set(k, v)
						return true
					})
				}
			}
		}
	}
	style := computeLayoutStyle(fd)
	if style != "" {
		existing := ""
		if ev, ok := out.Get("style"); ok {
			existing = fmt.Sprintf("%v", ev)
		}
		if existing != "" {
			style = strings.TrimSpace(existing)
			if !strings.HasSuffix(style, ";") {
				style += ";"
			}
			style += " " + computeLayoutStyle(fd)
		}
		out.Set("style", style)
	}
	if out.Len() == 0 {
		return nil
	}
	return out
}

func computeLayoutStyle(fd *formatDef) string {
	if fd == nil {
		return ""
	}
	parts := []string{}
	switch fd.Layout {
	case "flex", "stack":
		parts = append(parts, "display:flex")
		if fd.Layout == "stack" {
			parts = append(parts, "flex-direction:column")
		}
		if fd.Wrap {
			parts = append(parts, "flex-wrap:wrap")
		}
	case "grid":
		parts = append(parts, "display:grid")
		if fd.Columns != "" {
			parts = append(parts, "grid-template-columns:"+fd.Columns)
		}
	}
	if fd.Gap != "" {
		parts = append(parts, "gap:"+fd.Gap)
	}
	if fd.Align != "" {
		parts = append(parts, "align-items:"+fd.Align)
	}
	if fd.Justify != "" {
		parts = append(parts, "justify-content:"+fd.Justify)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + ";"
}

func fillDefaultVars(fd *formatDef, vars map[string]string) map[string]string {
	if vars == nil {
		vars = make(map[string]string)
	}
	if fd == nil || fd.Defaults == nil {
		return vars
	}
	fd.Defaults.Range(func(k string, v interface{}) bool {
		if _, ok := vars[k]; !ok {
			vars[k] = fmt.Sprintf("%v", v)
		}
		return true
	})
	return vars
}

func missingRequiredVars(fd *formatDef, vars map[string]string) []string {
	if fd == nil || len(fd.Required) == 0 {
		return nil
	}
	var missing []string
	for _, k := range fd.Required {
		if vars == nil || vars[k] == "" {
			missing = append(missing, k)
		}
	}
	return missing
}

// hasVarSubstitution checks if a format def uses $key/$value in params or ParamsWildcard.
func hasVarSubstitution(fd *formatDef) bool {
	if fd.ParamsWildcard {
		return true
	}
	if fd.Params == nil {
		return false
	}
	found := false
	fd.Params.Range(func(_ string, v interface{}) bool {
		if strings.Contains(fmt.Sprintf("%v", v), "$") {
			found = true
			return false
		}
		return true
	})
	return found
}

// usesKeyValueVars returns true if the format def references $key or $value
// in its params or contents. This indicates an iteration pattern where each
// map entry produces its own tag, rather than a single-entry pattern where
// the map's keys are named variable substitutions.
func usesKeyValueVars(fd *formatDef) bool {
	if fd.Params != nil {
		found := false
		fd.Params.Range(func(_ string, v interface{}) bool {
			s := fmt.Sprintf("%v", v)
			if strings.Contains(s, "$key") || strings.Contains(s, "$value") {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	if strings.Contains(fd.Contents, "$key") || strings.Contains(fd.Contents, "$value") {
		return true
	}
	return false
}

// formatParamsWithVars renders format def params as an HTML attribute string,
// substituting $varname from the vars map. Unreplaced $vars are omitted.
func formatParamsWithVars(params *OrderedMap, vars map[string]string) string {
	if params == nil || params.Len() == 0 {
		return ""
	}
	var sb strings.Builder
	params.Range(func(k string, v interface{}) bool {
		rendered := substituteVars(fmt.Sprintf("%v", v), vars)
		// Skip attributes that still contain unreplaced $vars
		if strings.Contains(rendered, "$") {
			return true
		}
		fmt.Fprintf(&sb, " %s=\"%s\"", k, html.EscapeString(rendered))
		return true
	})
	return sb.String()
}

// formatMapAsAttrs renders a map's entries directly as HTML attributes.
func formatMapAsAttrs(m *OrderedMap) string {
	var sb strings.Builder
	m.Range(func(k string, v interface{}) bool {
		fmt.Fprintf(&sb, " %s=\"%s\"", k, html.EscapeString(fmt.Sprintf("%v", v)))
		return true
	})
	return sb.String()
}

// hasUnreplacedVars returns true if s still contains $varname tokens.
func hasUnreplacedVars(s string) bool {
	return strings.Contains(s, "$")
}

// extractVarNames returns all $varname references in a string.
func extractVarNames(s string) []string {
	var names []string
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) {
			j := i + 1
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9' || s[j] == '_' || s[j] == '*') {
				j++
			}
			if j > i+1 {
				names = append(names, s[i+1:j])
			}
		}
	}
	return names
}

// substituteContentWrap replaces "$*" strings in a content wrapper structure
// with the actual content. This allows format definitions to specify structural
// wrappers, e.g., content: {card-body: '$*'} wraps children in card-body.
func substituteContentWrap(wrap interface{}, content interface{}) interface{} {
	switch w := wrap.(type) {
	case string:
		if w == "$*" {
			return content
		}
		return w
	case *OrderedMap:
		result := NewOrderedMap()
		w.Range(func(k string, v interface{}) bool {
			result.Set(k, substituteContentWrap(v, content))
			return true
		})
		return result
	case []interface{}:
		result := make([]interface{}, len(w))
		for i, v := range w {
			result[i] = substituteContentWrap(v, content)
		}
		return result
	default:
		return w
	}
}

// substituteVars replaces $varname tokens in s using the vars map.
func substituteVars(s string, vars map[string]string) string {
	if !strings.Contains(s, "$") || vars == nil {
		return s
	}
	for k, v := range vars {
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}
