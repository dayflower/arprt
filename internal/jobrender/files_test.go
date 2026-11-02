package jobrender

import (
	"context"
	api "github.com/dayflower/arprt/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"reflect"
	"testing"
)

func TestInputVolumeJob(t *testing.T) {
	r, d, s, _ := fixture(t)
	src := s.Spec.Source.Job
	src.Volumes = []api.JobVolume{
		{Name: "config-all", ConfigMap: &api.JobConfigMapVolumeSource{Name: "config-all"}},
		{Name: "config-items", ConfigMap: &api.JobConfigMapVolumeSource{Name: "config-items", DefaultMode: ptr.To(int32(0440)), Optional: ptr.To(false), Items: []api.JobKeyToPath{{Key: "config", Path: "nested/config.json", Mode: ptr.To(int32(0400))}}}},
		{Name: "secret-all", Secret: &api.JobSecretVolumeSource{SecretName: "secret-all", Optional: ptr.To(true)}},
		{Name: "secret-items", Secret: &api.JobSecretVolumeSource{SecretName: "secret-items", DefaultMode: ptr.To(int32(0444)), Items: []api.JobKeyToPath{{Key: "token", Path: "renamed"}}}},
	}
	for _, v := range src.Volumes {
		src.Renderer.VolumeMounts = append(src.Renderer.VolumeMounts, api.JobVolumeMount{Name: v.Name, MountPath: "/etc/" + v.Name, ReadOnly: true})
	}
	if err := r.Client.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	st := load(t, r, d)
	j := r.Config.job(st)
	pod := j.Spec.Template.Spec
	if j.Namespace != s.Namespace || len(pod.Volumes) != 5 || len(pod.Containers[0].VolumeMounts) != 6 {
		t.Fatal("input volume count or namespace")
	}
	if !reflect.DeepEqual(pod.Volumes[2].ConfigMap, &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "config-items"}, DefaultMode: ptr.To(int32(0440)), Optional: ptr.To(false), Items: []corev1.KeyToPath{{Key: "config", Path: "nested/config.json", Mode: ptr.To(int32(0400))}}}) {
		t.Fatal("ConfigMap fields changed")
	}
	if pod.Volumes[1].ConfigMap.Items != nil || pod.Volumes[1].ConfigMap.DefaultMode != nil || pod.Volumes[1].ConfigMap.Optional != nil || !*pod.Volumes[3].Secret.Optional || pod.Volumes[4].Secret.Items[0].Path != "renamed" {
		t.Fatal("standard defaults or Secret fields changed")
	}
	for _, m := range pod.Containers[0].VolumeMounts[2:] {
		if !m.ReadOnly || m.SubPath != "" {
			t.Fatal("input mount is not read only")
		}
	}
	if *pod.SecurityContext.RunAsUser != 1000 || *pod.SecurityContext.RunAsGroup != 1000 || *pod.SecurityContext.FSGroup != 1000 || *pod.AutomountServiceAccountToken || !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem || *pod.Containers[0].SecurityContext.AllowPrivilegeEscalation || !reflect.DeepEqual(pod.Containers[0].SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatal("renderer security contract changed")
	}
	// Preparation succeeds with no ConfigMap/Secret objects in the API client.
	// Recovery retains the same attempt and its references, without fetching data.
	old := st.Active.ID
	step(t, r, d)
	if load(t, r, d).Active.ID != old {
		t.Fatal("recovery replaced matching input job")
	}
}
