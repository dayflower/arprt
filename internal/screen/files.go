package screen

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func validateJobInputs(source api.JobSource) error {
	if len(source.Volumes) > 16 || len(source.Renderer.VolumeMounts) > 16 {
		return errors.New("too many input volumes or mounts")
	}
	names := map[string]bool{}
	for _, v := range source.Volumes {
		if len(validation.IsDNS1123Label(v.Name)) > 0 || v.Name == "data" || names[v.Name] {
			return errors.New("invalid, duplicate or reserved input volume name")
		}
		names[v.Name] = true
		// Clear the allowlist and reject any remaining standard source.
		unsupported := v
		unsupported.Name = ""
		unsupported.ConfigMap = nil
		unsupported.Secret = nil
		if unsupported != (api.JobVolume{}) {
			return errors.New("unsupported input volume source")
		}
		if (v.ConfigMap == nil) == (v.Secret == nil) {
			return errors.New("exactly one input volume source is required")
		}
		var name string
		var items []api.JobKeyToPath
		var mode *int32
		if v.ConfigMap != nil {
			name = v.ConfigMap.Name
			items = v.ConfigMap.Items
			mode = v.ConfigMap.DefaultMode
		} else {
			name = v.Secret.SecretName
			items = v.Secret.Items
			mode = v.Secret.DefaultMode
		}
		if len(validation.IsDNS1123Subdomain(name)) > 0 || !validMode(mode) || len(items) > 128 {
			return errors.New("invalid input reference, mode or item count")
		}
		for _, item := range items {
			if len(validation.IsConfigMapKey(item.Key)) > 0 || !validMode(item.Mode) || item.Path == "" || utf8.RuneCountInString(item.Path) > 512 || strings.ContainsRune(item.Path, 0) || path.IsAbs(item.Path) || item.Path == "." || strings.HasPrefix(item.Path, "..") {
				return errors.New("invalid input key, path or mode")
			}
			for _, p := range strings.Split(item.Path, "/") {
				if p == ".." {
					return errors.New("input item path escapes volume")
				}
			}
		}
	}
	paths := []string{source.ContextPath, path.Dir(source.ImagePath)}
	for _, m := range source.Renderer.VolumeMounts {
		if !names[m.Name] || !m.ReadOnly || m.SubPath != nil || m.SubPathExpr != nil || m.MountPropagation != nil || m.RecursiveReadOnly != nil {
			return errors.New("invalid input mount reference or unsupported mount options")
		}
		p := m.MountPath
		if utf8.RuneCountInString(p) > 512 || !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00:") {
			return errors.New("invalid input mount path")
		}
		for _, other := range paths {
			if p == other || strings.HasPrefix(p, other+"/") || strings.HasPrefix(other, p+"/") {
				return errors.New("input mount overlaps another mount")
			}
		}
		paths = append(paths, p)
	}
	return nil
}

func validMode(mode *int32) bool { return mode == nil || *mode >= 0 && *mode <= 0777 }
