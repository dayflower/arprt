// crd-chart synchronizes generated CRDs and checks their Helm representation.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const canonicalDir = "config/crd/bases"
const chartDir = "charts/arprt-crds"

func main() {
	check := flag.Bool("check", false, "check embedded bytes and rendered CRDs without changing files")
	helm := flag.String("helm", "helm", "Helm executable used for rendering")
	flag.Parse()
	if err := run(*check, *helm); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(check bool, helm string) error {
	sources, err := readFiles(canonicalDir)
	if err != nil {
		return err
	}
	embeddedDir := filepath.Join(chartDir, "files", "crds")
	if !check {
		return synchronize(sources, embeddedDir)
	}
	if err := checkFiles(sources, embeddedDir); err != nil {
		return err
	}
	// Different release metadata must never affect cluster-wide names or specs.
	for _, release := range []struct{ name, namespace string }{
		{"arprt-crds", "arprt"}, {"custom-release", "custom-namespace"},
	} {
		args := []string{"template", release.name, chartDir, "--namespace", release.namespace, "--kube-version", "1.36.0"}
		output, err := exec.Command(helm, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("helm template: %w\n%s", err, output)
		}
		if err := checkRendered(sources, output, release.name, release.namespace); err != nil {
			return err
		}
	}
	fmt.Println("PASS: CRD chart file set, embedded bytes, rendered specs and Helm metadata")
	return nil
}

func readFiles(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		// Both directories contain generated YAML only. Reject unexpected entries
		// instead of allowing unverified files to escape the chart's glob.
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".yaml") {
			return nil, fmt.Errorf("unexpected CRD file: %s", filepath.Join(dir, entry.Name()))
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no CRDs in %s", dir)
	}
	return files, nil
}

func synchronize(sources map[string][]byte, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected CRD file: %s", filepath.Join(dir, entry.Name()))
		}
		if _, exists := sources[entry.Name()]; !exists {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	for name, data := range sources {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func checkFiles(sources map[string][]byte, dir string) error {
	embedded, err := readFiles(dir)
	if err != nil {
		return err
	}
	if len(sources) != len(embedded) {
		return fmt.Errorf("CRD chart file set differs; run make manifests")
	}
	for name, data := range sources {
		if !bytes.Equal(data, embedded[name]) {
			return fmt.Errorf("CRD chart source differs: %s; run make manifests", name)
		}
	}
	return nil
}

func decodeCRDs(data []byte) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if len(obj) == 0 {
			continue
		}
		metadata, _ := obj["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		if obj["apiVersion"] != "apiextensions.k8s.io/v1" || obj["kind"] != "CustomResourceDefinition" || name == "" || obj["spec"] == nil {
			return nil, fmt.Errorf("expected a named v1 CRD with a spec")
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("duplicate CRD: %s", name)
		}
		result[name] = obj
	}
	return result, nil
}

func checkRendered(sources map[string][]byte, rendered []byte, release, namespace string) error {
	canonical := map[string]map[string]any{}
	for filename, data := range sources {
		objects, err := decodeCRDs(data)
		if err != nil {
			return fmt.Errorf("%s: %w", filename, err)
		}
		if len(objects) != 1 {
			return fmt.Errorf("expected one CRD in %s", filename)
		}
		for name, obj := range objects {
			if _, duplicate := canonical[name]; duplicate {
				return fmt.Errorf("duplicate canonical CRD: %s", name)
			}
			canonical[name] = obj
		}
	}
	objects, err := decodeCRDs(rendered)
	if err != nil {
		return err
	}
	if len(objects) != len(canonical) {
		return fmt.Errorf("rendered CRD set differs")
	}
	for name, source := range canonical {
		obj, exists := objects[name]
		if !exists || !reflect.DeepEqual(source["spec"], obj["spec"]) {
			return fmt.Errorf("rendered CRD name/spec differs: %s", name)
		}
		metadata := obj["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		annotations, _ := metadata["annotations"].(map[string]any)
		if metadata["namespace"] != nil || labels["app.kubernetes.io/managed-by"] != "Helm" ||
			annotations["meta.helm.sh/release-name"] != release ||
			annotations["meta.helm.sh/release-namespace"] != namespace ||
			annotations["helm.sh/resource-policy"] != "keep" || annotations["helm.sh/hook"] != nil {
			return fmt.Errorf("rendered Helm metadata differs: %s", name)
		}
		// The template may add management metadata, but must preserve all other
		// canonical content, including future controller-gen annotations.
		sourceMetadata := source["metadata"].(map[string]any)
		for _, key := range []string{"labels", "annotations"} {
			original, _ := sourceMetadata[key].(map[string]any)
			actual, _ := metadata[key].(map[string]any)
			for k, v := range original {
				if !reflect.DeepEqual(v, actual[k]) {
					return fmt.Errorf("rendered CRD metadata differs: %s %s.%s", name, key, k)
				}
			}
		}
	}
	return nil
}
