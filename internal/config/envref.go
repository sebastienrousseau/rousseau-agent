package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// envRef matches `${NAME}` exactly. Bare `$NAME` is left alone so a
// password that happens to contain a dollar sign survives.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ErrUnsetEnvRef is returned when a `${NAME}` reference names a
// variable that is not set. The README has documented this syntax
// for api_key and similar fields since v0.0.2; before this the
// literal string was sent to the provider.
var ErrUnsetEnvRef = errors.New("config: environment variable referenced but not set")

// expandEnvRefs walks every string field in cfg (through nested
// structs, pointers, slices and maps) and replaces `${NAME}` with the
// variable's value. An unset variable is an error rather than an
// empty string: a blank API key or bearer token fails later and
// further from the cause.
func expandEnvRefs(cfg *Config) error {
	missing := map[string]struct{}{}
	expandValue(reflect.ValueOf(cfg).Elem(), missing)
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("%w: %s", ErrUnsetEnvRef, strings.Join(names, ", "))
}

func expandValue(v reflect.Value, missing map[string]struct{}) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() && strings.Contains(v.String(), "${") {
			v.SetString(expandString(v.String(), missing))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			expandValue(v.Elem(), missing)
		}
	case reflect.Struct:
		expandStruct(v, missing)
	case reflect.Slice, reflect.Array:
		expandSlice(v, missing)
	case reflect.Map:
		expandMap(v, missing)
	default:
	}
}

func expandStruct(v reflect.Value, missing map[string]struct{}) {
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).IsExported() {
			expandValue(v.Field(i), missing)
		}
	}
}

func expandSlice(v reflect.Value, missing map[string]struct{}) {
	for i := 0; i < v.Len(); i++ {
		expandValue(v.Index(i), missing)
	}
}

// expandMap handles map[string]string and map[string]<struct>: map
// values are not addressable, so each is copied, expanded and stored
// back.
func expandMap(v reflect.Value, missing map[string]struct{}) {
	if v.IsNil() {
		return
	}
	for _, key := range v.MapKeys() {
		elem := v.MapIndex(key)
		cp := reflect.New(elem.Type()).Elem()
		cp.Set(elem)
		expandValue(cp, missing)
		v.SetMapIndex(key, cp)
	}
}

func expandString(s string, missing map[string]struct{}) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		val, ok := os.LookupEnv(name)
		if !ok {
			missing[name] = struct{}{}
			return m
		}
		return val
	})
}
