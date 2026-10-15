//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/credentials"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// A real Helm release exercises retention and field ownership. Envtest has no
// storage provisioner or kubelet: mounted data and delivery need cluster tests.
func TestControllerChartStateLifecycle(t *testing.T) {
	helm := os.Getenv("ARPRT_HELM")
	if helm == "" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("run make integration to provision Helm and API server binaries")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, api.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const namespace, release = "state-test", "home"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"test": {ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}},
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test", Namespace: namespace}},
		CurrentContext: "test",
	}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	helmCommand := func(args ...string) {
		t.Helper()
		args = append(args, "--kubeconfig", kubeconfig, "--namespace", namespace)
		command := exec.CommandContext(ctx, helm, args...)
		command.Env = append(os.Environ(), "HELM_DRIVER=secret")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("helm %v: %v\n%s", args, err, output)
		}
	}
	// Assemble from current source in isolation, ignoring any stale local archive.
	for _, name := range []string{"arprt", "arprt-crds"} {
		if err := os.CopyFS(filepath.Join(dir, name), os.DirFS(filepath.Join("..", "..", "charts", name))); err != nil {
			t.Fatal(err)
		}
	}
	chart := filepath.Join(dir, "arprt")
	if err := os.RemoveAll(filepath.Join(chart, "charts")); err != nil {
		t.Fatal(err)
	}
	helmCommand("dependency", "build", chart, "--skip-refresh")
	valuesFile := filepath.Join(dir, "values.yaml")
	writeValues := func(settings string) {
		t.Helper()
		// CRDs belong to envtest; this release must never adopt them.
		values := "crds:\n  enabled: false\nbyos:\n  publicBaseURL: https://byos.example.test/arprt\n" + settings
		if err := os.WriteFile(valuesFile, []byte(values), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const configured = "credentials:\n  authSecrets:\n    - name: generated-auth\n      create: true\n    - name: external-auth\n      create: false\n  identitySecrets:\n    - external-identity\n"
	create := func(obj client.Object) {
		t.Helper()
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	externalAuth := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "external-auth", Namespace: namespace}, Type: corev1.SecretTypeOpaque}
	externalIdentity := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "external-identity", Namespace: namespace}, Type: corev1.SecretTypeOpaque}
	externalClaim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "external-data", Namespace: namespace},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{}},
	}
	// The retained claim is reused later; a separately created claim checks that
	// existingClaim does not attach Helm ownership to an external object.
	writeValues(configured)
	create(externalAuth)
	create(externalIdentity)
	helmCommand("install", release, chart, "--values", valuesFile, "--wait=false")
	var auth corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "generated-auth"}, &auth); err != nil {
		t.Fatal(err)
	}
	if len(auth.Data) != 0 || len(auth.StringData) != 0 || auth.Type != corev1.SecretTypeOpaque {
		t.Fatal("chart supplied credential data instead of an empty Opaque Secret")
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Name: "device", Namespace: namespace}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: auth.Name}}}
	create(d)
	saConfig := rest.CopyConfig(cfg)
	saConfig.Impersonate.UserName = "system:serviceaccount:" + namespace + ":home-arprt"
	sa, err := client.New(saConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	// Use the setup implementation with the chart's real ServiceAccount rights.
	key, err := credentials.Ensure(ctx, sa, sa, d)
	if err != nil {
		t.Fatal("chart permissions prevented credential provisioning:", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&auth), &auth); err != nil {
		t.Fatal(err)
	}
	var claim corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "home-arprt-data"}, &claim); err != nil {
		t.Fatal(err)
	}
	assertRetained := func() {
		t.Helper()
		for _, original := range []client.Object{&auth, &claim} {
			stored := original.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, client.ObjectKeyFromObject(original), stored); err != nil || stored.GetUID() != original.GetUID() {
				t.Fatalf("%T was deleted or replaced: %v", original, err)
			}
			annotations := stored.GetAnnotations()
			if annotations["helm.sh/resource-policy"] != "keep" || annotations["meta.helm.sh/release-name"] != release || annotations["meta.helm.sh/release-namespace"] != namespace {
				t.Fatalf("%T lost retention/ownership metadata", original)
			}
		}
		if got, err := credentials.Read(ctx, c, d); err != nil || got != key {
			t.Fatal("Helm deleted or replaced the generated credential", err)
		}
		for _, original := range []client.Object{externalAuth, externalIdentity, externalClaim} {
			if original.GetUID() == "" {
				continue
			}
			stored := original.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, client.ObjectKeyFromObject(original), stored); err != nil || !reflect.DeepEqual(original, stored) {
				t.Fatalf("Helm modified external %T: %v", original, err)
			}
		}
	}
	assertAccess := func(name, verb string, allowed bool) {
		t.Helper()
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		var err error
		switch verb {
		case "get":
			err = sa.Get(ctx, client.ObjectKeyFromObject(secret), secret)
		case "patch":
			err = sa.Patch(ctx, secret, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"access-check":"true"}}}`)))
		case "create":
			err = sa.Create(ctx, secret)
		case "list":
			err = sa.List(ctx, &corev1.SecretList{}, client.InNamespace(namespace))
		}
		if (allowed && err != nil) || (!allowed && !apierrors.IsForbidden(err)) {
			t.Fatalf("unexpected Secret %s permission for %s: %v", verb, name, err)
		}
	}
	assertAccess("external-auth", "get", true)
	assertAccess("external-identity", "get", true)
	assertAccess("external-identity", "patch", false)
	assertAccess("undeclared", "get", false)
	assertAccess("undeclared", "patch", false)
	assertAccess("undeclared", "create", false)
	assertAccess("undeclared", "list", false)
	otherNamespace := &corev1.Secret{}
	if err := sa.Get(ctx, client.ObjectKey{Namespace: "default", Name: auth.Name}, otherNamespace); !apierrors.IsForbidden(err) {
		t.Fatal("chart allowed cross-namespace Secret access:", err)
	}
	writeValues(configured + "display:\n  deviceRefreshSeconds: 600\n")
	helmCommand("upgrade", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	helmCommand("uninstall", release, "--wait=false")
	assertRetained()
	// Same owner resumes management without take-ownership or credential lookup.
	helmCommand("install", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	// Stop managing the Secret without revoking its explicitly named access.
	writeValues(strings.Replace(configured, "create: true", "create: false", 1))
	helmCommand("upgrade", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	assertAccess(auth.Name, "get", true)
	// Removing a Secret entry retains the object/key and revokes its permissions.
	writeValues("")
	helmCommand("upgrade", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	assertAccess(auth.Name, "get", false)
	assertAccess(auth.Name, "patch", false)
	// Restore the entry under the original owner, then reuse the retained PVC.
	writeValues(configured + "persistence:\n  existingClaim: home-arprt-data\n")
	helmCommand("upgrade", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	helmCommand("uninstall", release, "--wait=false")
	assertRetained()
	helmCommand("install", release, chart, "--values", valuesFile, "--wait=false")
	assertRetained()
	// Check truly external storage with Jobs disabled as well.
	externalClaim.Spec = *claim.Spec.DeepCopy()
	create(externalClaim)
	writeValues(configured + "persistence:\n  existingClaim: external-data\njobs:\n  enabled: false\n")
	helmCommand("upgrade", release, chart, "--values", valuesFile, "--wait=false")
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "home-arprt"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != externalClaim.Name {
		t.Fatal("deployment did not reuse existingClaim with Jobs disabled")
	}
	assertRetained()
	helmCommand("uninstall", release, "--wait=false")
	assertRetained()
}
