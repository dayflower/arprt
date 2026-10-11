package jobrender

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const managedLabel = "arprt.dayflower.github.io/renderer"
const deviceLabel = "arprt.dayflower.github.io/device"

type Config struct {
	Refresh         time.Duration
	Namespace       string
	PVC             string
	ControllerPod   string
	DeadlineSeconds int64
	MaxActive       int
}

func (c Config) ValidateTopology(ctx context.Context, r client.Reader, root string) error {
	if c.PVC == "" || c.ControllerPod == "" || c.DeadlineSeconds < 1 || c.MaxActive < 1 {
		return errors.New("Job rendering requires a PVC, controller Pod, positive deadline and concurrency")
	}
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.PVC}, &pvc); err != nil {
		return err
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce || (pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) {
		return errors.New("Job rendering supports only filesystem ReadWriteOnce PVCs")
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.ControllerPod}, &pod); err != nil {
		return err
	}
	if pod.Labels["app.kubernetes.io/name"] != "arprt" || pod.Spec.NodeName == "" {
		return errors.New("controller Pod must be scheduled and labeled app.kubernetes.io/name=arprt")
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != c.PVC {
			continue
		}
		for _, container := range pod.Spec.Containers {
			for _, m := range container.VolumeMounts {
				if m.Name == v.Name && m.MountPath == root && m.SubPath == "" && m.SubPathExpr == "" && !m.ReadOnly {
					return nil
				}
			}
		}
	}
	return errors.New("controller must mount the whole Job PVC writable at artifact-root")
}

func normalize(source api.JobSource) (api.JobSource, error) {
	if source.ContextPath == "" {
		source.ContextPath = "/arprt/context.json"
	}
	if source.ImagePath == "" {
		source.ImagePath = "/arprt/output.png"
	}
	for _, p := range []string{source.ContextPath, source.ImagePath} {
		if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
			return source, errors.New("invalid Job file path")
		}
		if p == "/var/lib/arprt" || strings.HasPrefix(p, "/var/lib/arprt/") {
			return source, errors.New("reserved artifact path")
		}
	}
	if source.ContextPath == source.ImagePath || strings.HasPrefix(source.ContextPath, source.ImagePath+"/") || strings.HasPrefix(source.ImagePath, source.ContextPath+"/") {
		return source, errors.New("overlapping Job files")
	}
	parent := path.Dir(source.ImagePath)
	for _, reserved := range []string{"/", "/proc", "/sys", "/dev", "/etc", "/bin", "/sbin", "/usr", "/var/lib/arprt", "/var/run/secrets", "/run/secrets"} {
		if parent == reserved || (reserved != "/" && (strings.HasPrefix(parent, reserved+"/") || strings.HasPrefix(reserved, parent+"/"))) {
			return source, errors.New("output mount overlaps a reserved directory")
		}
	}
	if source.Renderer.Image == "" {
		return source, errors.New("renderer image is required")
	}
	return source, nil
}

func (c Config) job(st *state) *batchv1.Job {
	a := st.Active
	src := a.Source
	renderer := src.Renderer
	labels := map[string]string{managedLabel: "true", deviceLabel: hash([]byte(st.DeviceUID))[:32]}
	resources := *renderer.Resources.DeepCopy()
	if resources.Requests == nil {
		resources.Requests = corev1.ResourceList{}
	}
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	for k, v := range (corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}) {
		if _, ok := resources.Limits[k]; !ok {
			resources.Limits[k] = v
		}
	}
	for k, v := range (corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}) {
		if _, ok := resources.Requests[k]; !ok {
			if limit := resources.Limits[k]; limit.Cmp(v) < 0 {
				v = limit
			}
			resources.Requests[k] = v
		}
	}
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "arprt-render-" + a.ID, Namespace: st.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "ArprtDevice", Name: st.DeviceName, UID: types.UID(st.DeviceUID), Controller: ptr.To(true)}}}, Spec: batchv1.JobSpec{
		BackoffLimit: ptr.To(int32(0)), ActiveDeadlineSeconds: ptr.To(c.DeadlineSeconds), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Affinity:        &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "arprt"}}, Namespaces: []string{st.Namespace}, TopologyKey: "kubernetes.io/hostname"}}}},
			Containers: []corev1.Container{{Name: "renderer", Image: renderer.Image, Command: renderer.Command, Args: renderer.Args, Env: renderer.Env, EnvFrom: renderer.EnvFrom, Resources: resources,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: path.Dir(src.ImagePath), SubPath: "runs/" + a.ID + "/output"}, {Name: "data", MountPath: src.ContextPath, SubPath: "runs/" + a.ID + "/context.json", ReadOnly: true}},
			}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c.PVC}}}},
		}},
	}}
}
