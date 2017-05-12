// Copyright 2017 Santhosh Kumar Tekuri. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jsonschema

import (
	"regexp"

	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/loader"
)

// A Compiler represents a draft4 json-schema compiler.
type Compiler struct {
	resources map[string]*resource
}

// NewCompiler returns a draft4 json-schema Compiler object.
func NewCompiler() *Compiler {
	return &Compiler{make(map[string]*resource)}
}

// AddResource adds in-memory resource to the compiler.
func (c *Compiler) AddResource(url string, data []byte) error {
	r, err := newResource(url, data)
	if err != nil {
		return err
	}
	c.resources[r.url] = r
	return nil
}

// Compile parses json-schema at given url returns, if successful,
// a Schema object that can be used to match against json.
//
// The json-schema is validated with draft4 specification.
func (c *Compiler) Compile(url string) (*Schema, error) {
	base, fragment := split(url)
	if _, ok := c.resources[base]; !ok {
		data, err := loader.Load(base)
		if err != nil {
			return nil, err
		}
		r, err := newResource(base, data)
		if err != nil {
			return nil, err
		}
		c.resources[r.url] = r
	}
	r := c.resources[base]
	s, err := c.compileRef(r, nil, r.url, fragment)
	return s, err
}

func validateSchema(url string, v interface{}) error {
	if draft4 != nil {
		if err := draft4.validate(v); err != nil {
			finishContext(err, draft4)
			return &SchemaError{url, err}
		}
	}
	return nil
}

func (c Compiler) compileRef(r *resource, root map[string]interface{}, base, ref string) (*Schema, error) {
	if rootFragment(ref) {
		if _, ok := r.schemas["#"]; !ok {
			if err := validateSchema(r.url, r.doc); err != nil {
				return nil, err
			}
			hash := "#"
			s := &Schema{url: &r.url, ptr: &hash}
			r.schemas["#"] = s
			m := r.doc.(map[string]interface{})
			if _, err := c.compile(r, s, base, m, m); err != nil {
				return nil, err
			}
		}
		return r.schemas["#"], nil
	}

	ids := make(map[string]interface{})
	resolveIDs(r.url, root, ids)

	if strings.HasPrefix(ref, "#/") {
		if _, ok := r.schemas[ref]; !ok {
			ptrBase, doc, err := r.resolvePtr(ref)
			if err != nil {
				return nil, err
			}
			if err := validateSchema(r.url, doc); err != nil {
				return nil, err
			}
			r.schemas[ref] = &Schema{url: &base, ptr: &ref}
			m := doc.(map[string]interface{})

			if _, err := c.compile(r, r.schemas[ref], ptrBase, root, m); err != nil {
				return nil, err
			}
		}
		return r.schemas[ref], nil
	}

	refURL, err := resolveURL(base, ref)
	if err != nil {
		return nil, err
	}
	if rs, ok := r.schemas[refURL]; ok {
		return rs, nil
	}

	if v, ok := ids[refURL]; ok {
		if err := validateSchema(r.url, v); err != nil {
			return nil, err
		}
		u, f := split(refURL)
		s := &Schema{url: &u, ptr: &f}
		r.schemas[refURL] = s
		rmap := v.(map[string]interface{})
		if _, err := c.compile(r, s, refURL, root, rmap); err != nil {
			return nil, err
		}
		return s, nil
	}

	base, _ = split(refURL)
	if base == r.url {
		return nil, fmt.Errorf("invalid ref: %q", refURL)
	}
	return c.Compile(refURL)
}

func (c Compiler) compile(r *resource, s *Schema, base string, root, m map[string]interface{}) (*Schema, error) {
	var err error

	if s == nil {
		s = new(Schema)
	}

	if id, ok := m["id"]; ok {
		if base, err = resolveURL(base, id.(string)); err != nil {
			return nil, err
		}
	}

	if ref, ok := m["$ref"]; ok {
		s.ref, err = c.compileRef(r, root, base, ref.(string))
		if err != nil {
			return nil, err
		}
		// All other properties in a "$ref" object MUST be ignored
		return s, nil
	}

	if t, ok := m["type"]; ok {
		switch t := t.(type) {
		case string:
			s.types = []string{t}
		case []interface{}:
			s.types = make([]string, len(t))
			for i, v := range t {
				s.types[i] = v.(string)
			}
		}
	}

	if e, ok := m["enum"]; ok {
		s.enum = e.([]interface{})
	}

	if not, ok := m["not"]; ok {
		s.not, err = c.compile(r, nil, base, root, not.(map[string]interface{}))
		if err != nil {
			return nil, err
		}
	}

	loadSchemas := func(pname string) ([]*Schema, error) {
		if pvalue, ok := m[pname]; ok {
			pvalue := pvalue.([]interface{})
			schemas := make([]*Schema, len(pvalue))
			for i, v := range pvalue {
				sch, err := c.compile(r, nil, base, root, v.(map[string]interface{}))
				if err != nil {
					return nil, err
				}
				schemas[i] = sch
			}
			return schemas, nil
		}
		return nil, nil
	}
	if s.allOf, err = loadSchemas("allOf"); err != nil {
		return nil, err
	}
	if s.anyOf, err = loadSchemas("anyOf"); err != nil {
		return nil, err
	}
	if s.oneOf, err = loadSchemas("oneOf"); err != nil {
		return nil, err
	}

	loadInt := func(pname string) int {
		if num, ok := m[pname]; ok {
			return int(num.(float64))
		}
		return -1
	}
	s.minProperties, s.maxProperties = loadInt("minProperties"), loadInt("maxProperties")

	if req, ok := m["required"]; ok {
		req := req.([]interface{})
		s.required = make([]string, len(req))
		for i, pname := range req {
			s.required[i] = pname.(string)
		}
	}

	if props, ok := m["properties"]; ok {
		props := props.(map[string]interface{})
		s.properties = make(map[string]*Schema, len(props))
		for pname, pmap := range props {
			s.properties[pname], err = c.compile(r, nil, base, root, pmap.(map[string]interface{}))
			if err != nil {
				return nil, err
			}
		}
	}

	if regexProps, ok := m["regexProperties"]; ok {
		s.regexProperties = regexProps.(bool)
	}

	if patternProps, ok := m["patternProperties"]; ok {
		patternProps := patternProps.(map[string]interface{})
		s.patternProperties = make(map[*regexp.Regexp]*Schema, len(patternProps))
		for pattern, pmap := range patternProps {
			s.patternProperties[regexp.MustCompile(pattern)], err = c.compile(r, nil, base, root, pmap.(map[string]interface{}))
			if err != nil {
				return nil, err
			}
		}
	}

	if additionalProps, ok := m["additionalProperties"]; ok {
		switch additionalProps := additionalProps.(type) {
		case bool:
			if !additionalProps {
				s.additionalProperties = false
			}
		case map[string]interface{}:
			s.additionalProperties, err = c.compile(r, nil, base, root, additionalProps)
			if err != nil {
				return nil, err
			}
		}
	}

	if deps, ok := m["dependencies"]; ok {
		deps := deps.(map[string]interface{})
		s.dependencies = make(map[string]interface{}, len(deps))
		for pname, pvalue := range deps {
			switch pvalue := pvalue.(type) {
			case map[string]interface{}:
				s.dependencies[pname], err = c.compile(r, nil, base, root, pvalue)
				if err != nil {
					return nil, err
				}
			case []interface{}:
				props := make([]string, len(pvalue))
				for i, prop := range pvalue {
					props[i] = prop.(string)
				}
				s.dependencies[pname] = props
			}
		}
	}

	s.minItems, s.maxItems = loadInt("minItems"), loadInt("maxItems")

	if unique, ok := m["uniqueItems"]; ok {
		s.uniqueItems = unique.(bool)
	}

	if items, ok := m["items"]; ok {
		switch items := items.(type) {
		case map[string]interface{}:
			s.items, err = c.compile(r, nil, base, root, items)
			if err != nil {
				return nil, err
			}
		case []interface{}:
			schemas := make([]*Schema, 0, len(items))
			for _, item := range items {
				ischema, err := c.compile(r, nil, base, root, item.(map[string]interface{}))
				if err != nil {
					return nil, err
				}
				schemas = append(schemas, ischema)
			}
			s.items = schemas
		}

		if additionalItems, ok := m["additionalItems"]; ok {
			switch additionalItems := additionalItems.(type) {
			case bool:
				s.additionalItems = additionalItems
			case map[string]interface{}:
				s.additionalItems, err = c.compile(r, nil, base, root, additionalItems)
				if err != nil {
					return nil, err
				}
			}
		} else {
			s.additionalItems = true
		}
	}

	s.minLength, s.maxLength = loadInt("minLength"), loadInt("maxLength")

	if pattern, ok := m["pattern"]; ok {
		s.pattern = regexp.MustCompile(pattern.(string))
	}

	if format, ok := m["format"]; ok {
		s.format = format.(string)
	}

	loadFloat := func(pname string) *float64 {
		if num, ok := m[pname]; ok {
			num := num.(float64)
			return &num
		}
		return nil
	}

	s.minimum = loadFloat("minimum")
	if s.minimum != nil {
		if exclusive, ok := m["exclusiveMinimum"]; ok {
			s.exclusiveMinimum = exclusive.(bool)
		}
	}

	s.maximum = loadFloat("maximum")
	if s.maximum != nil {
		if exclusive, ok := m["exclusiveMaximum"]; ok {
			s.exclusiveMaximum = exclusive.(bool)
		}
	}

	s.multipleOf = loadFloat("multipleOf")

	return s, nil
}
