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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
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
	fmt.Println("PASS: chart render matrix, invalid values, deployment contracts and self-contained packages")
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
					if err := check(objects, name.release, name.namespace, bundled, existing, jobs, expectedImage); err != nil {
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
			if err := check(objects, "home", "custom-space", true, false, true, expectedImage); err != nil {
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

func check(objects []object, release, namespace string, bundled, existing, jobs bool, image string) error {
	var deployment appsv1.Deployment
	if err := one(objects, "Deployment", &deployment); err != nil {
		return err
	}
	name := release + "-arprt"
	pod := deployment.Spec.Template.Spec
	if len(pod.Containers) != 1 || deployment.Spec.Replicas == nil {
		return fmt.Errorf("expected one controller container and replica setting")
	}
	controller := pod.Containers[0]
	expectedPodSecurity := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	expectedSecurity := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	expectedResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("probes")}}}
	}

	claim := name + "-data"
	if existing {
		claim = "external-data"
	}
	labels := map[string]string{"app.kubernetes.io/name": "arprt", "app.kubernetes.io/instance": release}
	if err := require(deployment.Name == name && deployment.Namespace == namespace && *deployment.Spec.Replicas == 1 && deployment.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType &&
		reflect.DeepEqual(deployment.Spec.Selector.MatchLabels, labels) && reflect.DeepEqual(deployment.Spec.Template.Labels, labels) && pod.ServiceAccountName == name &&
		reflect.DeepEqual(pod.SecurityContext, expectedPodSecurity) && reflect.DeepEqual(controller.SecurityContext, expectedSecurity) &&
		reflect.DeepEqual(controller.VolumeMounts, []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/arprt"}}) && controller.Image == image &&
		reflect.DeepEqual(controller.Resources, expectedResources) && reflect.DeepEqual(controller.LivenessProbe, probe("/healthz")) && reflect.DeepEqual(controller.ReadinessProbe, probe("/readyz")) &&
		len(pod.Volumes) == 1 && pod.Volumes[0].PersistentVolumeClaim != nil && pod.Volumes[0].Name == "data" && !pod.Volumes[0].PersistentVolumeClaim.ReadOnly && pod.Volumes[0].PersistentVolumeClaim.ClaimName == claim &&
		pod.TerminationGracePeriodSeconds != nil && *pod.TerminationGracePeriodSeconds == 30 &&
		reflect.DeepEqual(controller.Ports, []corev1.ContainerPort{{Name: "byos", ContainerPort: 8080}, {Name: "metrics", ContainerPort: 8082}, {Name: "probes", ContainerPort: 8081}}), "Deployment violates fixed security/storage/runtime contracts"); err != nil {
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
	expectedEnvironment := map[string]corev1.EnvVarSource{
		"POD_NAMESPACE": {FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		"POD_NAME":      {FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
	}
	for env, key := range map[string]string{
		"PUBLIC_BASE_URL": "publicBaseURL", "PROXY_STRIPS_PREFIX": "proxyStripsPrefix", "ENABLE_SETUP": "setupEnabled",
		"DEVICE_REFRESH_SECONDS": "deviceRefreshSeconds", "OVERLAY_TIME_ZONE": "overlayTimeZone", "STORAGE_MAX_BYTES": "storageMaxBytes",
		"STORAGE_MIN_FREE_BYTES": "storageMinFreeBytes", "JOB_CONCURRENCY": "jobConcurrency", "JOB_DEADLINE_SECONDS": "jobDeadlineSeconds",
	} {
		expectedEnvironment[env] = corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: config.Name}, Key: key}}
	}
	for _, env := range controller.Env {
		expected, ok := expectedEnvironment[env.Name]
		if !ok || env.ValueFrom == nil || env.Value != "" || !reflect.DeepEqual(*env.ValueFrom, expected) {
			return fmt.Errorf("broken runtime environment reference %s", env.Name)
		}
		delete(expectedEnvironment, env.Name)
		if env.Name != "PUBLIC_BASE_URL" && !strings.HasPrefix(env.Name, "POD_") && !contains(controller.Args, "--"+map[string]string{"PROXY_STRIPS_PREFIX": "proxy-strips-prefix", "ENABLE_SETUP": "enable-setup", "DEVICE_REFRESH_SECONDS": "device-refresh-seconds", "OVERLAY_TIME_ZONE": "overlay-time-zone", "STORAGE_MAX_BYTES": "storage-max-bytes", "STORAGE_MIN_FREE_BYTES": "storage-min-free-bytes", "JOB_CONCURRENCY": "job-concurrency", "JOB_DEADLINE_SECONDS": "job-deadline-seconds"}[env.Name]+"=$("+env.Name+")") {
			return fmt.Errorf("unwired setting %s", env.Name)
		}
	}
	if len(expectedEnvironment) != 0 {
		return fmt.Errorf("missing runtime environment references")
	}
	var role rbacv1.Role
	if err := one(objects, "Role", &role); err != nil {
		return err
	}
	// Exact grants are independent of installation manifests; deny additional
	// resources, wildcard names/verbs and cluster-wide alternatives.
	expectedRole := rbacv1.Role{Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get", "list", "watch", "create", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods", "persistentvolumeclaims"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{"", "events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"living-room-auth"}, Verbs: []string{"get", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"living-room-identity"}, Verbs: []string{"get"}},
		{APIGroups: []string{"arprt.dayflower.github.io"}, Resources: []string{"arprtscreens", "arprtdevices", "arprtplaylists"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"arprt.dayflower.github.io"}, Resources: []string{"arprtscreens/status", "arprtdevices/status", "arprtplaylists/status"}, Verbs: []string{"get", "patch", "update"}},
	}}
	if !reflect.DeepEqual(permissions(role), permissions(expectedRole)) {
		return fmt.Errorf("RBAC violates explicit namespace/named Secret grants")
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
			var pvc corev1.PersistentVolumeClaim
			if err := one(objects, "PersistentVolumeClaim", &pvc); err != nil {
				return err
			}
			if pvc.Name != claim || !reflect.DeepEqual(pvc.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) ||
				pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem ||
				pvc.Spec.StorageClassName != nil || pvc.Spec.Resources.Requests.Storage().String() != "1Gi" ||
				pvc.Spec.Selector != nil || pvc.Spec.DataSource != nil || pvc.Spec.DataSourceRef != nil || pvc.Spec.VolumeName != "" {
				return fmt.Errorf("PVC violates filesystem RWO contract")
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
