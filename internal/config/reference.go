package config

import (
	"reflect"
	"strings"
)

// YAMLKey describes one key accepted in openlore.yml, derived from the yaml
// struct tags on fileConfig. Path is dotted; a list of objects is written with
// a "[]" suffix on the list segment (for example "oidc_issuers[].issuer_url").
// Section is true for keys that only group other keys and hold no value.
type YAMLKey struct {
	Path    string
	Type    string
	Section bool
}

// YAMLKeys returns every key openlore.yml accepts, in struct order. It is the
// source of truth for the generated configuration reference, so documented
// keys cannot drift from the spelling the loader actually reads.
func YAMLKeys() []YAMLKey {
	var keys []YAMLKey
	walkYAMLKeys(reflect.TypeOf(fileConfig{}), "", &keys)
	return keys
}

func walkYAMLKeys(t reflect.Type, prefix string, keys *[]YAMLKey) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() && field.Tag.Get("yaml") == "" {
			continue
		}
		name := yamlFieldName(field)
		if name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			*keys = append(*keys, YAMLKey{Path: path, Type: "object", Section: true})
			walkYAMLKeys(ft, path, keys)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			*keys = append(*keys, YAMLKey{Path: path, Type: "list of objects", Section: true})
			walkYAMLKeys(ft.Elem(), path+"[]", keys)
		default:
			*keys = append(*keys, YAMLKey{Path: path, Type: yamlTypeName(ft)})
		}
	}
}

// yamlFieldName mirrors gopkg.in/yaml.v3: the tag name wins, otherwise the
// lower-cased Go field name is the key.
func yamlFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("yaml")
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return name
	}
	return strings.ToLower(field.Name)
}

func yamlTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Slice:
		return "list of " + yamlTypeName(t.Elem()) + "s"
	default:
		return t.Kind().String()
	}
}
