package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestObjectRendering(t *testing.T) {
	var out bytes.Buffer
	minItems := int64(1)
	node := apiextensionsv1.JSONSchemaProps{
		Required: []string{"source"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"source": {Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"method": {Type: "string", Default: &apiextensionsv1.JSON{Raw: []byte(`"GET"`)}, Enum: []apiextensionsv1.JSON{{Raw: []byte(`"GET"`)}, {Raw: []byte(`"POST"`)}}, Description: "GET | POST\n<endpoint> & input"},
			}},
			"entries": {Type: "array", MinItems: &minItems, Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object", Required: []string{"name"}, Properties: map[string]apiextensionsv1.JSONSchemaProps{"name": {Type: "string"}},
			}}},
		},
		XValidations: apiextensionsv1.ValidationRules{{Rule: "true", Message: "exactly one source is required"}},
	}
	renderObject(&out, "Example v1 ", "spec", node)
	text := out.String()
	for _, want := range []string{
		"Validation failure messages:\n\n- exactly one source is required",
		"[`source`](#example-v1-specsource) | object | Yes |",
		"[`entries`](#example-v1-specentries) | array of object | No |",
		"Minimum items: 1.", "### Example v1 spec.entries[]",
		"| `name` | string | Yes |", "### Example v1 spec.source",
		"| `method` | string | No | `\"GET\"` |",
		"GET &#124; POST &lt;endpoint&gt; &amp; input", "Allowed: `\"GET\"`, `\"POST\"`.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// Sorting must not depend on Go map iteration order.
	var repeated bytes.Buffer
	renderObject(&repeated, "Example v1 ", "spec", node)
	if repeated.String() != text {
		t.Fatal("rendering is nondeterministic")
	}
}

func TestArrayItemConstraints(t *testing.T) {
	text := constraints(apiextensionsv1.JSONSchemaProps{Type: "array", Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{
		Type: "string", Enum: []apiextensionsv1.JSON{{Raw: []byte(`"timestamp"`)}, {Raw: []byte(`"battery"`)}},
	}}})
	if text != "Each item: Allowed: `\"timestamp\"`, `\"battery\"`." {
		t.Fatal(text)
	}
}

func TestPresentationBoundaries(t *testing.T) {
	standard := apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"nested": {Type: "string"}}}
	var out bytes.Buffer
	renderObject(&out, "Screen v1 ", "spec.source.job.renderer", apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{"resources": standard}})
	if strings.Contains(out.String(), "`nested`") || !strings.Contains(out.String(), "Standard Kubernetes type") {
		t.Fatal(out.String())
	}
	out.Reset()
	renderObject(&out, "Screen v1 ", "spec.source.job.volumes[]", apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{
		"name": {Type: "string"}, "secret": standard, "hostPath": standard,
	}})
	if strings.Contains(out.String(), "hostPath") || !strings.Contains(out.String(), "`secret`") || !strings.Contains(out.String(), "`nested`") {
		t.Fatal(out.String())
	}
}

func TestRenderResourceIdentityAndMissingSchema(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Group: "example.test", Scope: apiextensionsv1.NamespaceScoped,
		Names:    apiextensionsv1.CustomResourceDefinitionNames{Kind: "Example", Plural: "examples"},
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true}},
	}}
	if _, err := render([]apiextensionsv1.CustomResourceDefinition{crd}); err == nil {
		t.Fatal("missing schema was accepted")
	}
	crd.Spec.Versions[0].Schema = &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {Type: "object"}, "status": {Type: "object"},
	}}}
	data, err := render([]apiextensionsv1.CustomResourceDefinition{crd})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Example v1", "`example.test/v1`", "`Namespaced`", "### Example v1 status"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestCheckDetectsStaleReferenceWithoutWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, dir := range []string{"config/crd/bases", "notes"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	crd := apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Group: "example.test", Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "Example"},
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Schema: &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object"}, "status": {Type: "object"}}},
		}}},
	}}
	data, err := yaml.Marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("config/crd/bases", "example.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(false); err != nil {
		t.Fatal(err)
	}
	if err := run(true); err != nil {
		t.Fatal(err)
	}
	stale := []byte("manually edited\n")
	if err := os.WriteFile(outputPath, stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale reference error, got %v", err)
	}
	current, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, stale) {
		t.Fatal("check mode changed the reference")
	}
}
