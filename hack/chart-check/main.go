// Command chart-check verifies rendered and packaged controller contracts.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

type object map[string]any

func main() {
	helm := flag.String("helm", "helm", "Helm executable")
	packages := flag.String("packages", "bin/charts", "assembled chart packages")
	flag.Parse()
	if err := run(*helm, *packages); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: chart render matrix, invalid values, Kustomize contracts and self-contained packages")
}

func command(helm string, args ...string) ([]byte, error) {
	cmd := exec.Command(helm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return output, nil
}

func decode(data []byte) ([]object, error) {
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var objects []object
	for {
		var obj object
		if err := decoder.Decode(&obj); err == io.EOF {
			return objects, nil
		} else if err != nil {
			return nil, err
		}
		if len(obj) != 0 {
			objects = append(objects, obj)
		}
	}
}

func readObjects(path string) ([]object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(data)
}

func one(objects []object, kind string, target any) error {
	var found []object
	for _, obj := range objects {
		if obj["kind"] == kind {
			found = append(found, obj)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("expected one %s, got %d", kind, len(found))
	}
	data, err := json.Marshal(found[0])
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("%s", message)
	}
	return nil
}

func render(helm, chart, release, namespace, values string, extra ...string) ([]object, error) {
	args := []string{"template", release, chart, "--namespace", namespace, "--kube-version", "1.36.0", "--values", values}
	output, err := command(helm, append(args, extra...)...)
	if err != nil {
		return nil, err
	}
	return decode(output)
}

func run(helm, packages string) error {
	dir, err := os.MkdirTemp("", "arprt-chart-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	values := filepath.Join(dir, "values.json")
	write := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(values, data, 0o600)
	}
	defaults, err := readObjects("charts/arprt/values.yaml")
	if err != nil {
		return err
	}
	defaultImage := defaults[0]["image"].(map[string]any)
	expectedImage := fmt.Sprint(defaultImage["repository"], "@", defaultImage["digest"])
	base := []object{}
	for _, filename := range []string{"deployment.yaml", "rbac.yaml", "pvc.yaml", "service.yaml"} {
		objs, err := readObjects(filepath.Join("config/default", filename))
		if err != nil {
			return err
		}
		base = append(base, objs...)
	}
	for _, name := range []struct{ release, namespace string }{{"arprt", "arprt"}, {"home", "custom-space"}} {
		for _, bundled := range []bool{true, false} {
			for _, existing := range []bool{false, true} {
				for _, jobs := range []bool{true, false} {
					settings := map[string]any{
						"byos": map[string]any{"publicBaseURL": "https://byos.example.test/arprt"},
						"crds": map[string]any{"enabled": bundled}, "jobs": map[string]any{"enabled": jobs},
						"credentials": map[string]any{"authSecrets": []any{map[string]any{"name": "living-room-auth", "create": !existing}}, "identitySecrets": []string{"living-room-identity"}},
					}
					if existing {
						settings["persistence"] = map[string]any{"existingClaim": "external-data"}
					}
					if err := write(settings); err != nil {
						return err
					}
					objects, err := render(helm, "charts/arprt", name.release, name.namespace, values)
					if err != nil {
						return err
					}
					if err := check(objects, base, name.release, name.namespace, bundled, existing, jobs, expectedImage); err != nil {
						return fmt.Errorf("release=%s bundled=%t existing=%t jobs=%t: %w", name.release, bundled, existing, jobs, err)
					}
				}
			}
		}
	}
	// Defaults remain credential-free. Non-default runtime settings must change
	// both the ConfigMap wiring and its rollout checksum; tags work without digest.
	if err := write(map[string]any{"byos": map[string]any{"publicBaseURL": "https://byos.example.test/arprt"}}); err != nil {
		return err
	}
	original, err := render(helm, "charts/arprt", "home", "custom-space", values)
	if err != nil {
		return err
	}
	var originalDeployment appsv1.Deployment
	if err := one(original, "Deployment", &originalDeployment); err != nil {
		return err
	}
	for _, obj := range original {
		if obj["kind"] == "Secret" {
			return fmt.Errorf("default chart creates credentials")
		}
	}
	var role rbacv1.Role
	if err := one(original, "Role", &role); err != nil {
		return err
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" {
				return fmt.Errorf("default chart grants Secret access")
			}
		}
	}
	changed, err := render(helm, "charts/arprt", "home", "custom-space", values,
		"--set", "image.digest=,image.tag=test,metrics.enabled=false,byos.setupEnabled=false,byos.proxyStripsPrefix=true,display.deviceRefreshSeconds=600,display.overlayTimeZone=Asia/Tokyo,storage.maxBytes=900000000,storage.minFreeBytes=0,jobs.concurrency=2,jobs.deadlineSeconds=60,persistence.storageClass=,persistence.size=2Gi")
	if err != nil {
		return err
	}
	var deployment appsv1.Deployment
	if err := one(changed, "Deployment", &deployment); err != nil {
		return err
	}
	var config corev1.ConfigMap
	if err := one(changed, "ConfigMap", &config); err != nil {
		return err
	}
	var pvc corev1.PersistentVolumeClaim
	if err := one(changed, "PersistentVolumeClaim", &pvc); err != nil {
		return err
	}
	if err := require(deployment.Spec.Template.Annotations["checksum/config"] != originalDeployment.Spec.Template.Annotations["checksum/config"] &&
		deployment.Spec.Template.Spec.Containers[0].Image == fmt.Sprint(defaultImage["repository"], ":test") &&
		contains(deployment.Spec.Template.Spec.Containers[0].Args, "--metrics-bind-address=0") && len(deployment.Spec.Template.Spec.Containers[0].Ports) == 2 &&
		pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName == "" && pvc.Spec.Resources.Requests.Storage().String() == "2Gi", "non-default image/metrics/storage/rollout contract differs"); err != nil {
		return err
	}
	expectedConfig := map[string]string{"publicBaseURL": "https://byos.example.test/arprt", "proxyStripsPrefix": "true", "setupEnabled": "false", "deviceRefreshSeconds": "600", "overlayTimeZone": "Asia/Tokyo", "storageMaxBytes": "900000000", "storageMinFreeBytes": "0", "jobConcurrency": "2", "jobDeadlineSeconds": "60"}
	if !reflect.DeepEqual(config.Data, expectedConfig) {
		return fmt.Errorf("non-default runtime configuration differs: %v", config.Data)
	}
	invalid := []struct {
		settings object
		field    string
	}{
		{object{"byos": object{"publicBaseURL": ""}}, "publicBaseURL"},
		{object{"byos": object{"publicBaseURL": "https://user:pass@example.test"}}, "publicBaseURL"},
		{object{"byos": object{"publicBaseURL": "https://example.test/a/../b"}}, "publicBaseURL"},
		{object{"byos": object{"publicBaseURL": "https://example.test:65536"}}, "publicBaseURL"},
		{object{"image": object{"tag": "", "digest": ""}}, "image"},
		{object{"image": object{"digest": "sha256:bad"}}, "digest"},
		{object{"replicaCount": 2}, "replicaCount"}, {object{"namespace": "other"}, "namespace"},
		{object{"persistence": object{"accessModes": []string{"ReadWriteMany"}}}, "accessModes"},
		{object{"persistence": object{"existingClaim": "external", "size": "2Gi"}}, "size"},
		{object{"persistence": object{"existingClaim": "external", "storageClass": "fast"}}, "storageClass"},
		{object{"display": object{"overlayTimeZone": "Local"}}, "overlayTimeZone"},
		{object{"jobs": object{"concurrency": 0}}, "concurrency"},
		{object{"credentials": object{"authSecrets": []any{object{"name": "same", "create": true}, object{"name": "same", "create": false}}}}, "duplicate"},
		{object{"credentials": object{"authSecrets": []any{object{"name": "auth", "create": true, "apiKey": "forbidden"}}}}, "apiKey"},
	}
	for _, test := range invalid {
		if _, ok := test.settings["byos"]; !ok {
			test.settings["byos"] = object{"publicBaseURL": "https://byos.example.test/arprt"}
		}
		if err := write(test.settings); err != nil {
			return err
		}
		_, err := render(helm, "charts/arprt", "home", "custom-space", values)
		if err == nil || !strings.Contains(err.Error(), test.field) {
			return fmt.Errorf("invalid %s did not fail clearly: %v", test.field, err)
		}
	}
	// Render copies of only the packaged archives, with no sibling local source.
	if err := write(object{"byos": object{"publicBaseURL": "https://byos.example.test/arprt"}, "credentials": object{"authSecrets": []any{object{"name": "living-room-auth", "create": true}}, "identitySecrets": []string{"living-room-identity"}}}); err != nil {
		return err
	}
	for _, chart := range []string{"arprt", "arprt-crds"} {
		metadata, err := readObjects(filepath.Join("charts", chart, "Chart.yaml"))
		if err != nil {
			return err
		}
		archive := chart + "-" + metadata[0]["version"].(string) + ".tgz"
		data, err := os.ReadFile(filepath.Join(packages, archive))
		if err != nil {
			return err
		}
		target := filepath.Join(dir, archive)
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
		objects, err := render(helm, target, "home", "custom-space", values)
		if err != nil {
			return err
		}
		if chart == "arprt" {
			if err := check(objects, base, "home", "custom-space", true, false, true, expectedImage); err != nil {
				return err
			}
		} else {
			source, err := render(helm, "charts/arprt-crds", "home", "custom-space", values)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(objects, source) {
				return fmt.Errorf("packaged CRD chart differs from source")
			}
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// Normalize RBAC into granted tuples: grouping permissions into rules is immaterial.
func permissions(role rbacv1.Role) []string {
	var result []string
	for _, rule := range role.Rules {
		names := rule.ResourceNames
		if len(names) == 0 {
			names = []string{"*"}
		}
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					for _, name := range names {
						result = append(result, group+"/"+resource+"/"+verb+"/"+name)
					}
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

func check(objects, base []object, release, namespace string, bundled, existing, jobs bool, image string) error {
	var deployment, canonical appsv1.Deployment
	if err := one(objects, "Deployment", &deployment); err != nil {
		return err
	}
	if err := one(base, "Deployment", &canonical); err != nil {
		return err
	}
	name := release + "-arprt"
	pod, original := deployment.Spec.Template.Spec, canonical.Spec.Template.Spec
	if len(pod.Containers) != 1 || deployment.Spec.Replicas == nil {
		return fmt.Errorf("expected one controller container and replica setting")
	}
	controller, originalController := pod.Containers[0], original.Containers[0]
	claim := name + "-data"
	if existing {
		claim = "external-data"
	}
	labels := map[string]string{"app.kubernetes.io/name": "arprt", "app.kubernetes.io/instance": release}
	if err := require(deployment.Name == name && deployment.Namespace == namespace && *deployment.Spec.Replicas == 1 && deployment.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType &&
		reflect.DeepEqual(deployment.Spec.Selector.MatchLabels, labels) && reflect.DeepEqual(deployment.Spec.Template.Labels, labels) && pod.ServiceAccountName == name &&
		reflect.DeepEqual(pod.SecurityContext, original.SecurityContext) && reflect.DeepEqual(controller.SecurityContext, originalController.SecurityContext) &&
		reflect.DeepEqual(controller.VolumeMounts, originalController.VolumeMounts) && controller.Image == image &&
		reflect.DeepEqual(controller.Resources, originalController.Resources) && reflect.DeepEqual(controller.LivenessProbe, originalController.LivenessProbe) && reflect.DeepEqual(controller.ReadinessProbe, originalController.ReadinessProbe) &&
		len(pod.Volumes) == 1 && pod.Volumes[0].PersistentVolumeClaim != nil && pod.Volumes[0].PersistentVolumeClaim.ClaimName == claim, "Deployment differs from fixed Kustomize contracts"); err != nil {
		return err
	}
	jobClaim := ""
	if jobs {
		jobClaim = claim
	}
	for _, arg := range []string{"--namespace=$(POD_NAMESPACE)", "--artifact-root=/var/lib/arprt", "--job-pvc=" + jobClaim, "--metrics-bind-address=:8082"} {
		if !contains(controller.Args, arg) {
			return fmt.Errorf("missing runtime argument %s", arg)
		}
	}
	var config corev1.ConfigMap
	if err := one(objects, "ConfigMap", &config); err != nil {
		return err
	}
	expectedConfig := map[string]string{"publicBaseURL": "https://byos.example.test/arprt", "proxyStripsPrefix": "false", "setupEnabled": "true", "deviceRefreshSeconds": "300", "overlayTimeZone": "UTC", "storageMaxBytes": "805306368", "storageMinFreeBytes": "67108864", "jobConcurrency": "4", "jobDeadlineSeconds": "120"}
	if !reflect.DeepEqual(config.Data, expectedConfig) || len(controller.Env) != 11 || len(controller.Args) != 12 {
		return fmt.Errorf("runtime defaults or environment/argument set differs")
	}
	for _, env := range controller.Env {
		if env.ValueFrom == nil {
			return fmt.Errorf("literal runtime environment %s", env.Name)
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
			if ref.Name != config.Name || config.Data[ref.Key] == "" {
				return fmt.Errorf("broken ConfigMap reference %s", env.Name)
			}
			if env.Name != "PUBLIC_BASE_URL" && !contains(controller.Args, "--"+map[string]string{"PROXY_STRIPS_PREFIX": "proxy-strips-prefix", "ENABLE_SETUP": "enable-setup", "DEVICE_REFRESH_SECONDS": "device-refresh-seconds", "OVERLAY_TIME_ZONE": "overlay-time-zone", "STORAGE_MAX_BYTES": "storage-max-bytes", "STORAGE_MIN_FREE_BYTES": "storage-min-free-bytes", "JOB_CONCURRENCY": "job-concurrency", "JOB_DEADLINE_SECONDS": "job-deadline-seconds"}[env.Name]+"=$("+env.Name+")") {
				return fmt.Errorf("unwired setting %s", env.Name)
			}
		} else if ref := env.ValueFrom.FieldRef; ref == nil || (env.Name == "POD_NAMESPACE" && ref.FieldPath != "metadata.namespace") || (env.Name == "POD_NAME" && ref.FieldPath != "metadata.name") {
			return fmt.Errorf("broken downward API reference")
		}
	}
	var role, baseRole rbacv1.Role
	if err := one(objects, "Role", &role); err != nil {
		return err
	}
	if err := one(base, "Role", &baseRole); err != nil {
		return err
	}
	if !reflect.DeepEqual(permissions(role), permissions(baseRole)) {
		return fmt.Errorf("RBAC grants differ from Kustomize")
	}
	var account corev1.ServiceAccount
	if err := one(objects, "ServiceAccount", &account); err != nil {
		return err
	}
	if account.Name != pod.ServiceAccountName {
		return fmt.Errorf("broken ServiceAccount reference")
	}
	var binding rbacv1.RoleBinding
	if err := one(objects, "RoleBinding", &binding); err != nil {
		return err
	}
	var service corev1.Service
	if err := one(objects, "Service", &service); err != nil {
		return err
	}
	if err := require(binding.RoleRef.Name == role.Name && binding.RoleRef.Kind == "Role" && binding.RoleRef.APIGroup == rbacv1.GroupName && len(binding.Subjects) == 1 && binding.Subjects[0].Kind == "ServiceAccount" && binding.Subjects[0].Name == name && binding.Subjects[0].Namespace == namespace &&
		reflect.DeepEqual(service.Spec.Selector, labels) && len(service.Spec.Ports) == 1 && service.Spec.Ports[0].Port == 8080 && service.Spec.Ports[0].TargetPort.StrVal == "byos", "Service/RBAC references differ"); err != nil {
		return err
	}
	crds, pvcs, secrets := 0, 0, 0
	for _, obj := range objects {
		metadata := obj["metadata"].(map[string]any)
		switch obj["kind"] {
		case "CustomResourceDefinition":
			crds++
			source, err := readObjects(filepath.Join("config/crd/bases", "arprt.dayflower.github.io_"+strings.Split(metadata["name"].(string), ".")[0]+".yaml"))
			if err != nil {
				return err
			}
			annotations, _ := metadata["annotations"].(map[string]any)
			if metadata["namespace"] != nil || annotations["meta.helm.sh/release-name"] != release || annotations["meta.helm.sh/release-namespace"] != namespace {
				return fmt.Errorf("bundled CRD ownership differs")
			}
			if !reflect.DeepEqual(source[0]["spec"], obj["spec"]) {
				return fmt.Errorf("bundled CRD schema differs")
			}
		case "PersistentVolumeClaim":
			pvcs++
			var pvc, originalPVC corev1.PersistentVolumeClaim
			if err := one(objects, "PersistentVolumeClaim", &pvc); err != nil {
				return err
			}
			if err := one(base, "PersistentVolumeClaim", &originalPVC); err != nil {
				return err
			}
			// Kubernetes defaults an omitted volumeMode to Filesystem.
			filesystem := corev1.PersistentVolumeFilesystem
			if originalPVC.Spec.VolumeMode == nil {
				originalPVC.Spec.VolumeMode = &filesystem
			}
			if pvc.Name != claim || !reflect.DeepEqual(pvc.Spec, originalPVC.Spec) {
				return fmt.Errorf("PVC differs from filesystem RWO contract")
			}
		case "Secret":
			secrets++
			if metadata["name"] != "living-room-auth" || obj["type"] != "Opaque" || obj["data"] != nil || obj["stringData"] != nil {
				return fmt.Errorf("chart supplied or owned external credentials")
			}
		case "Deployment", "ConfigMap", "Role", "RoleBinding", "Service", "ServiceAccount":
		default:
			return fmt.Errorf("unexpected chart resource %v", obj["kind"])
		}
		if obj["kind"] == "CustomResourceDefinition" || obj["kind"] == "Secret" || obj["kind"] == "PersistentVolumeClaim" {
			annotations, _ := metadata["annotations"].(map[string]any)
			if annotations["helm.sh/resource-policy"] != "keep" {
				return fmt.Errorf("missing resource retention")
			}
		}
		if obj["kind"] != "CustomResourceDefinition" && metadata["namespace"] != namespace {
			return fmt.Errorf("namespace mismatch")
		}
	}
	expectedCRDs, expectedState := 0, 1
	if bundled {
		expectedCRDs = 3
	}
	if existing {
		expectedState = 0
	}
	return require(crds == expectedCRDs && pvcs == expectedState && secrets == expectedState, "created/external resource set differs")
}
