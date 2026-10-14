//go:build integration

package integration

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// This exercises only the CRD release against a disposable API server. The
// controller/Job/PVC deployment matrix belongs to the controller chart checks.
func TestCRDChartLifecycle(t *testing.T) {
	helm := os.Getenv("ARPRT_HELM")
	if helm == "" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("run make integration to provision Helm and API server binaries")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const namespace = "chart-test"
	const release = "custom-crds"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"test": {
			Server: cfg.Host, CertificateAuthorityData: cfg.CAData,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {
			ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData,
		}},
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test", Namespace: namespace}},
		CurrentContext: "test",
	}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	helmCommand := func(args ...string) {
		t.Helper()
		args = append(args, "--kubeconfig", kubeconfig, "--namespace", namespace)
		command := exec.CommandContext(ctx, helm, args...)
		// Ignore any developer-specific driver selection; this API server owns
		// all release records and never uses the selected kubeconfig context.
		command.Env = append(os.Environ(), "HELM_DRIVER=secret")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("helm %v: %v\n%s", args, err, output)
		}
	}
	chart := filepath.Join("..", "..", "charts", "arprt-crds")
	helmCommand("install", release, chart, "--wait", "--timeout", "60s")
	names := []string{
		"arprtdevices.arprt.dayflower.github.io",
		"arprtscreens.arprt.dayflower.github.io",
		"arprtplaylists.arprt.dayflower.github.io",
	}
	uids := map[string]types.UID{}
	for _, name := range names {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			t.Fatal(err)
		}
		if crd.Annotations["helm.sh/resource-policy"] != "keep" ||
			crd.Annotations["meta.helm.sh/release-name"] != release ||
			crd.Annotations["meta.helm.sh/release-namespace"] != namespace {
			t.Fatalf("unexpected live CRD metadata: %s", name)
		}
		uids[name] = crd.UID
	}
	playlist := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "arprt.dayflower.github.io/v1alpha1", "kind": "ArprtPlaylist",
			"metadata": map[string]any{"name": name, "namespace": namespace},
			"spec":     map[string]any{"screens": []any{map[string]any{"name": "sample"}}},
		}}
	}
	existing := playlist("existing")
	if err := c.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	existingUID := existing.GetUID()
	withNewField := playlist("new-field")
	withNewField.Object["spec"].(map[string]any)["optionalUpgradeField"] = "accepted"
	if err := c.Create(ctx, withNewField, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
		t.Fatal("old schema accepted the optional upgrade field")
	}
	// A compatible optional field exists only in a temporary chart fixture.
	fixture := filepath.Join(dir, "arprt-crds")
	if err := filepath.WalkDir(chart, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(chart, path)
		if err != nil {
			return err
		}
		target := filepath.Join(fixture, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	crdFile := filepath.Join(fixture, "files", "crds", "arprt.dayflower.github.io_arprtplaylists.yaml")
	data, err := os.ReadFile(crdFile)
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	properties := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties
	properties["optionalUpgradeField"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	data, err = yaml.Marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	write(crdFile, data)
	metadataFile := filepath.Join(fixture, "Chart.yaml")
	data, err = os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	write(metadataFile, []byte(strings.Replace(string(data), "version: 0.1.0", "version: 0.1.1", 1)))
	helmCommand("upgrade", release, fixture, "--wait", "--timeout", "60s")
	if err := c.Create(ctx, withNewField, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		t.Fatal("upgraded schema rejected optional field:", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(withNewField), withNewField); err != nil {
		t.Fatal(err)
	}
	if withNewField.Object["spec"].(map[string]any)["optionalUpgradeField"] != "accepted" {
		t.Fatal("upgraded field was pruned")
	}
	assertRetained := func() {
		t.Helper()
		for name, uid := range uids {
			var stored apiextensionsv1.CustomResourceDefinition
			if err := c.Get(ctx, client.ObjectKey{Name: name}, &stored); err != nil || stored.UID != uid {
				t.Fatalf("CRD %s was deleted or replaced: %v", name, err)
			}
		}
		stored := playlist("existing")
		if err := c.Get(ctx, client.ObjectKeyFromObject(stored), stored); err != nil || stored.GetUID() != existingUID {
			t.Fatalf("custom resource was deleted or replaced: %v", err)
		}
	}
	assertRetained()
	helmCommand("uninstall", release)
	assertRetained()
	// Reusing the same ownership metadata must not require forced adoption.
	helmCommand("install", release, fixture, "--wait", "--timeout", "60s")
	assertRetained()
	// Upgrades that remove a CRD from the release must also retain it.
	write(filepath.Join(fixture, "templates", "crds.yaml"), nil)
	helmCommand("upgrade", release, fixture, "--wait", "--timeout", "60s")
	assertRetained()
	helmCommand("uninstall", release)
	assertRetained()
}
