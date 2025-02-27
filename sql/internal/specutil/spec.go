// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package specutil

import (
	"fmt"

	"ariga.io/atlas/schemahcl"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlspec"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
)

// VarAttr is a helper method for constructing *schemahcl.Attr instances that contain a variable reference.
func VarAttr(k, v string) *schemahcl.Attr {
	return schemahcl.RefAttr(k, &schemahcl.Ref{V: v})
}

type (
	// SchemaSpec is returned by driver convert functions to
	// marshal a *schema.Schema into top-level spec objects.
	SchemaSpec struct {
		Schema *sqlspec.Schema
		Tables []*sqlspec.Table
		Views  []*sqlspec.View
	}
	doc struct {
		Tables  []*sqlspec.Table  `spec:"table"`
		Views   []*sqlspec.View   `spec:"view"`
		Schemas []*sqlspec.Schema `spec:"schema"`
	}
)

// Marshal marshals v into an Atlas DDL document using a schemahcl.Marshaler. Marshal uses the given
// schemaSpec function to convert a *schema.Schema into *sqlspec.Schema, []*sqlspec.Table and []*sqlspec.View.
func Marshal(v any, marshaler schemahcl.Marshaler, convertFunc func(*schema.Schema) (*SchemaSpec, error)) ([]byte, error) {
	d := &doc{}
	switch s := v.(type) {
	case *schema.Schema:
		spec, err := convertFunc(s)
		if err != nil {
			return nil, fmt.Errorf("specutil: failed converting schema to spec: %w", err)
		}
		d.Tables = spec.Tables
		d.Views = spec.Views
		d.Schemas = []*sqlspec.Schema{spec.Schema}
	case *schema.Realm:
		for _, s := range s.Schemas {
			spec, err := convertFunc(s)
			if err != nil {
				return nil, fmt.Errorf("specutil: failed converting schema to spec: %w", err)
			}
			d.Tables = append(d.Tables, spec.Tables...)
			d.Views = append(d.Views, spec.Views...)
			d.Schemas = append(d.Schemas, spec.Schema)
		}
		if err := QualifyTables(d.Tables); err != nil {
			return nil, err
		}
		if err := QualifyViews(d.Views); err != nil {
			return nil, err
		}
		if err := QualifyReferences(d.Tables, s); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("specutil: failed marshaling spec. %T is not supported", v)
	}
	return marshaler.MarshalSpec(d)
}

// QualifyTables sets the Qualifier field equal to the schema
// name in any tables with duplicate names in the provided specs.
func QualifyTables(specs []*sqlspec.Table) error {
	seen := make(map[string]*sqlspec.Table, len(specs))
	for _, t := range specs {
		if s, ok := seen[t.Name]; ok {
			schemaName, err := SchemaName(s.Schema)
			if err != nil {
				return err
			}
			s.Qualifier = schemaName
			schemaName, err = SchemaName(t.Schema)
			if err != nil {
				return err
			}
			t.Qualifier = schemaName
		}
		seen[t.Name] = t
	}
	return nil
}

// QualifyViews sets the Qualifier field equal to the schema
// name in any tables with duplicate names in the provided specs.
func QualifyViews(specs []*sqlspec.View) error {
	seen := make(map[string]*sqlspec.View, len(specs))
	for _, v := range specs {
		if s, ok := seen[v.Name]; ok {
			schemaName, err := SchemaName(s.Schema)
			if err != nil {
				return err
			}
			s.Qualifier = schemaName
			schemaName, err = SchemaName(v.Schema)
			if err != nil {
				return err
			}
			v.Qualifier = schemaName
		}
		seen[v.Name] = v
	}
	return nil
}

// QualifyReferences qualifies any reference with qualifier.
func QualifyReferences(tableSpecs []*sqlspec.Table, realm *schema.Realm) error {
	type cref struct{ s, t string }
	byRef := make(map[cref]*sqlspec.Table)
	for _, t := range tableSpecs {
		r := cref{s: t.Qualifier, t: t.Name}
		if byRef[r] != nil {
			return fmt.Errorf("duplicate references were found for: %v", r)
		}
		byRef[r] = t
	}
	for _, t := range tableSpecs {
		sname, err := SchemaName(t.Schema)
		if err != nil {
			return err
		}
		s1, ok := realm.Schema(sname)
		if !ok {
			return fmt.Errorf("schema %q was not found in realm", sname)
		}
		t1, ok := s1.Table(t.Name)
		if !ok {
			return fmt.Errorf("table %q.%q was not found in realm", sname, t.Name)
		}
		for _, fk := range t.ForeignKeys {
			fk1, ok := t1.ForeignKey(fk.Symbol)
			if !ok {
				return fmt.Errorf("table %q.%q.%q was not found in realm", sname, t.Name, fk.Symbol)
			}
			for i, c := range fk.RefColumns {
				if r, ok := byRef[cref{s: fk1.RefTable.Schema.Name, t: fk1.RefTable.Name}]; ok && r.Qualifier != "" {
					fk.RefColumns[i] = qualifiedExternalColRef(fk1.RefColumns[i].Name, r.Name, r.Qualifier)
				} else if r, ok := byRef[cref{t: fk1.RefTable.Name}]; ok && r.Qualifier == "" {
					fk.RefColumns[i] = externalColRef(fk1.RefColumns[i].Name, r.Name)
				} else {
					return fmt.Errorf("missing reference for column %q in %q.%q.%q", c.V, sname, t.Name, fk.Symbol)
				}
			}
		}
	}
	return nil
}

// HCLBytesFunc returns a helper that evaluates an HCL document from a byte slice instead
// of from an hclparse.Parser instance.
func HCLBytesFunc(ev schemahcl.Evaluator) func(b []byte, v any, inp map[string]cty.Value) error {
	return func(b []byte, v any, inp map[string]cty.Value) error {
		parser := hclparse.NewParser()
		if _, diag := parser.ParseHCL(b, ""); diag.HasErrors() {
			return diag
		}
		return ev.Eval(parser, v, inp)
	}
}
