package packagefs

import (
	"encoding"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

var jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
var jsonFieldCache sync.Map // reflect.Type -> jsonFieldSet

type jsonField struct {
	name  string
	typ   reflect.Type
	index []int
}

type jsonFieldSet struct {
	byName  map[string]jsonField
	ordered []jsonField
}

func jsonDestinationType(target reflect.Type) reflect.Type {
	for target != nil {
		if target.Implements(jsonUnmarshalerType) {
			return nil
		}
		if target.Kind() != reflect.Pointer {
			if reflect.PointerTo(target).Implements(jsonUnmarshalerType) {
				return nil
			}
			return target
		}
		target = target.Elem()
	}
	return nil
}

func jsonObjectField(target reflect.Type, key string) (reflect.Type, string) {
	if target == nil {
		return nil, ""
	}
	if target.Kind() == reflect.Map {
		return target.Elem(), jsonIntegerMapKey(target.Key(), key)
	}
	if target.Kind() != reflect.Struct {
		return nil, ""
	}
	fields := jsonFields(target)
	if field, ok := fields.byName[key]; ok {
		return field.typ, field.name
	}
	// Match encoding/json's first folded match only after considering every
	// exact name. EqualFold also handles Unicode aliases such as K and K.
	for _, field := range fields.ordered {
		if strings.EqualFold(field.name, key) {
			return field.typ, field.name
		}
	}
	return nil, ""
}

func jsonIntegerMapKey(keyType reflect.Type, key string) string {
	// Custom text decoders own their key identities. Do not invoke them during
	// validation or assume their numeric underlying type determines the result.
	if reflect.PointerTo(keyType).Implements(textUnmarshalerType) {
		return ""
	}
	switch keyType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value, err := strconv.ParseInt(key, 10, keyType.Bits()); err == nil {
			return strconv.FormatInt(value, 10)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value, err := strconv.ParseUint(key, 10, keyType.Bits()); err == nil {
			return strconv.FormatUint(value, 10)
		}
	}
	// Invalid keys are left to encoding/json's existing type and range errors.
	return ""
}

func jsonFields(target reflect.Type) jsonFieldSet {
	if cached, ok := jsonFieldCache.Load(target); ok {
		return cached.(jsonFieldSet)
	}
	type branch struct {
		typ   reflect.Type
		index []int
		count int
	}
	type choice struct {
		field  jsonField
		tagged bool
		count  int
	}
	choices := make(map[string]choice)
	visited := make(map[reflect.Type]bool)
	level := []branch{{typ: target, count: 1}}
	for len(level) > 0 {
		var next []branch
		positions := make(map[reflect.Type]int)
		for _, current := range level {
			if visited[current.typ] {
				continue
			}
			visited[current.typ] = true
			for index := 0; index < current.typ.NumField(); index++ {
				field := current.typ.Field(index)
				underlying := field.Type
				if underlying.Kind() == reflect.Pointer {
					underlying = underlying.Elem()
				}
				if !field.IsExported() && (!field.Anonymous || underlying.Kind() != reflect.Struct) {
					continue
				}
				tag := field.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				if !validJSONFieldTag(name) {
					name = ""
				}
				path := append(slices.Clone(current.index), index)
				if name == "" && field.Anonymous && underlying.Kind() == reflect.Struct {
					if position, exists := positions[underlying]; exists {
						next[position].count = 2
					} else {
						positions[underlying] = len(next)
						// encoding/json counts occurrences at each level of
						// promotion, independently of the parent's count.
						next = append(next, branch{underlying, path, 1})
					}
					continue
				}
				tagged := name != ""
				if name == "" {
					name = field.Name
				}
				candidate := choice{jsonField{name, field.Type, path}, tagged, current.count}
				previous, exists := choices[name]
				switch {
				case !exists:
					choices[name] = candidate
				case len(previous.field.index) != len(path):
					// A shallower declaration hides every deeper candidate.
				case tagged && !previous.tagged:
					choices[name] = candidate
				case tagged == previous.tagged:
					previous.count = 2
					choices[name] = previous
				}
			}
		}
		level = next
	}
	fields := make([]jsonField, 0, len(choices))
	for _, selected := range choices {
		if selected.count == 1 {
			fields = append(fields, selected.field)
		}
	}
	slices.SortFunc(fields, func(a, b jsonField) int { return slices.Compare(a.index, b.index) })
	set := jsonFieldSet{byName: make(map[string]jsonField, len(fields)), ordered: fields}
	for _, field := range fields {
		set.byName[field.name] = field
	}
	stored, _ := jsonFieldCache.LoadOrStore(target, set)
	return stored.(jsonFieldSet)
}

func validJSONFieldTag(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", char) {
			return false
		}
	}
	return true
}
