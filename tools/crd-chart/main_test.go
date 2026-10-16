package main

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSynchronization(t *testing.T) {
	dir := t.TempDir()
	sources := map[string][]byte{"a.yaml": []byte("canonical\n"), "b.yaml": []byte("second\n")}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := synchronize(sources, dir); err != nil {
		t.Fatal(err)
	}
	for _, drift := range []struct{ name, content string }{
		{"a.yaml", "changed\n"}, {"obsolete.yaml", "obsolete\n"}, {"unexpected.txt", "unexpected\n"},
	} {
		write(drift.name, drift.content)
		if err := checkFiles(sources, dir); err == nil {
			t.Fatalf("accepted drift: %s", drift.name)
		}
		if err := synchronize(sources, dir); err != nil {
			t.Fatal(err)
		}
		if err := checkFiles(sources, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "b.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := checkFiles(sources, dir); err == nil {
		t.Fatal("accepted missing file")
	}
	// A second generation must leave exactly the same bytes and file set.
	for range 2 {
		if err := synchronize(sources, dir); err != nil {
			t.Fatal(err)
		}
		if err := checkFiles(sources, dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderedContracts(t *testing.T) {
	const source = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: samples.example.test
  annotations:
    controller-gen.kubebuilder.io/version: v0.21.0
spec:
  group: example.test
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        properties:
          count:
            type: integer
            default: 0
    subresources:
      status: {}
`
	sources := map[string][]byte{"sample.yaml": []byte(source)}
	fixture := func() map[string]any {
		t.Helper()
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(source), &obj); err != nil {
			t.Fatal(err)
		}
		metadata := obj["metadata"].(map[string]any)
		metadata["labels"] = map[string]any{"app.kubernetes.io/managed-by": "Helm"}
		annotations := metadata["annotations"].(map[string]any)
		annotations["meta.helm.sh/release-name"] = "release"
		annotations["meta.helm.sh/release-namespace"] = "namespace"
		annotations["helm.sh/resource-policy"] = "keep"
		return obj
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"spec", func(obj map[string]any) { obj["spec"].(map[string]any)["group"] = "other.test" }},
		{"name", func(obj map[string]any) { obj["metadata"].(map[string]any)["name"] = "renamed.example.test" }},
		{"namespace", func(obj map[string]any) { obj["metadata"].(map[string]any)["namespace"] = "namespace" }},
		{"retention", func(obj map[string]any) {
			delete(obj["metadata"].(map[string]any)["annotations"].(map[string]any), "helm.sh/resource-policy")
		}},
		{"generator annotation", func(obj map[string]any) {
			delete(obj["metadata"].(map[string]any)["annotations"].(map[string]any), "controller-gen.kubebuilder.io/version")
		}},
		{"ownership", func(obj map[string]any) {
			obj["metadata"].(map[string]any)["annotations"].(map[string]any)["meta.helm.sh/release-name"] = "other"
		}},
		{"hook", func(obj map[string]any) {
			obj["metadata"].(map[string]any)["annotations"].(map[string]any)["helm.sh/hook"] = "pre-install"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := fixture()
			data, err := yaml.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkRendered(sources, data, "release", "namespace"); err != nil {
				t.Fatal("valid render rejected:", err)
			}
			tc.mutate(obj)
			data, err = yaml.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkRendered(sources, data, "release", "namespace"); err == nil {
				t.Fatal("accepted broken render")
			}
		})
	}
	obj := fixture()
	data, err := yaml.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append(append([]byte{}, data...), append([]byte("\n---\n"), data...)...)
	if err := checkRendered(sources, duplicate, "release", "namespace"); err == nil {
		t.Fatal("accepted duplicate CRD")
	}
	if err := checkRendered(sources, nil, "release", "namespace"); err == nil {
		t.Fatal("accepted empty render")
	}
}
