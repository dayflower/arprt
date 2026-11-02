package screen

import (
	api "github.com/dayflower/arprt/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"testing"
)

func fileSource() api.JobSource {
	return api.JobSource{Renderer: api.RendererContainer{Image: "busybox:1.37.0", VolumeMounts: []api.JobVolumeMount{{Name: "config", MountPath: "/etc/input", ReadOnly: true}, {Name: "auth", MountPath: "/etc/auth", ReadOnly: true}}}, Volumes: []api.JobVolume{{Name: "config", ConfigMap: &api.JobConfigMapVolumeSource{Name: "config", Items: []api.JobKeyToPath{{Key: "data", Path: "nested/config.json", Mode: ptr.To(int32(0440))}}}}, {Name: "auth", Secret: &api.JobSecretVolumeSource{SecretName: "auth", DefaultMode: ptr.To(int32(0400)), Optional: ptr.To(true)}}}}
}

func TestJobFileValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*api.JobSource)
	}{
		{"source missing", func(s *api.JobSource) { s.Volumes[0].ConfigMap = nil }},
		{"source conflict", func(s *api.JobSource) { s.Volumes[0].Secret = &api.JobSecretVolumeSource{SecretName: "auth"} }},
		{"hostPath", func(s *api.JobSource) { s.Volumes[0].HostPath = &corev1.HostPathVolumeSource{Path: "/tmp"} }},
		{"pvc", func(s *api.JobSource) {
			s.Volumes[0].PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}
		}},
		{"projected", func(s *api.JobSource) { s.Volumes[0].Projected = &corev1.ProjectedVolumeSource{} }},
		{"duplicate", func(s *api.JobSource) { s.Volumes[1].Name = "config" }},
		{"reserved", func(s *api.JobSource) { s.Volumes[0].Name = "data" }},
		{"invalid name", func(s *api.JobSource) { s.Volumes[0].Name = "UPPER" }},
		{"cross namespace", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Name = "other/config" }},
		{"missing ref", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].Name = "missing" }},
		{"read write", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].ReadOnly = false }},
		{"subPath", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].SubPath = ptr.To("file") }},
		{"subPathExpr", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].SubPathExpr = ptr.To("file") }},
		{"propagation", func(s *api.JobSource) {
			s.Renderer.VolumeMounts[0].MountPropagation = ptr.To(corev1.MountPropagationNone)
		}},
		{"recursive", func(s *api.JobSource) {
			s.Renderer.VolumeMounts[0].RecursiveReadOnly = ptr.To(corev1.RecursiveReadOnlyDisabled)
		}},
		{"relative", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "etc/input" }},
		{"unclean", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "/etc/../input" }},
		{"duplicate mount", func(s *api.JobSource) { s.Renderer.VolumeMounts[1].MountPath = "/etc/input" }},
		{"nested mount", func(s *api.JobSource) { s.Renderer.VolumeMounts[1].MountPath = "/etc/input/auth" }},
		{"parent mount", func(s *api.JobSource) { s.Renderer.VolumeMounts[1].MountPath = "/etc" }},
		{"context", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "/arprt/context.json" }},
		{"context child", func(s *api.JobSource) {
			s.ContextPath = "/context/file"
			s.Renderer.VolumeMounts[0].MountPath = "/context/file/child"
		}},
		{"context parent", func(s *api.JobSource) {
			s.ContextPath = "/context/file"
			s.Renderer.VolumeMounts[0].MountPath = "/context"
		}},
		{"output", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "/arprt" }},
		{"output child", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "/arprt/input" }},
		{"root", func(s *api.JobSource) { s.Renderer.VolumeMounts[0].MountPath = "/" }},
		{"absolute item", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items[0].Path = "/file" }},
		{"escape item", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items[0].Path = "dir/../file" }},
		{"reserved item", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items[0].Path = "..data/file" }},
		{"bad key", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items[0].Key = "../key" }},
		{"bad mode", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items[0].Mode = ptr.To(int32(01000)) }},
		{"bad default mode", func(s *api.JobSource) { s.Volumes[1].Secret.DefaultMode = ptr.To(int32(-1)) }},
		{"too many volumes", func(s *api.JobSource) { s.Volumes = make([]api.JobVolume, 17) }},
		{"too many mounts", func(s *api.JobSource) { s.Renderer.VolumeMounts = make([]api.JobVolumeMount, 17) }},
		{"too many items", func(s *api.JobSource) { s.Volumes[0].ConfigMap.Items = make([]api.JobKeyToPath, 129) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fileSource()
			tc.change(&s)
			if _, err := NormalizeJob(s); err == nil {
				t.Fatal("invalid file input accepted")
			}
		})
	}
	s := fileSource()
	got, err := NormalizeJob(s)
	if err != nil {
		t.Fatal(err)
	}
	got.Volumes[0].ConfigMap.Items[0].Path = "changed"
	if s.Volumes[0].ConfigMap.Items[0].Path == "changed" {
		t.Fatal("source aliases normalized copy")
	}
	spec := api.ArprtScreenSpec{Source: api.ScreenSource{Job: &s}}
	before, err := InputRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	s.Volumes[1].Secret.SecretName = "rotated"
	after, err := InputRevision(spec)
	if err != nil || before == after {
		t.Fatal("input reference did not change revision", err)
	}
}
