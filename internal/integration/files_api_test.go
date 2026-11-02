//go:build integration

package integration

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fileAPICase struct {
	name   string
	change func(map[string]any, map[string]any, map[string]any)
	valid  bool
}

func verifyJobFilesAPI(t *testing.T, c client.Client) {
	volume := func() map[string]any {
		return map[string]any{"name": "config", "configMap": map[string]any{"name": "config", "items": []any{map[string]any{"key": "config.json", "path": "nested/config.json", "mode": int64(0440)}}, "defaultMode": int64(0444), "optional": false}}
	}
	mount := func() map[string]any {
		return map[string]any{"name": "config", "mountPath": "/etc/input", "readOnly": true}
	}
	cases := []fileAPICase{
		{"valid", func(_, _, _ map[string]any) {}, true},
		{"both", func(v, _, _ map[string]any) { v["secret"] = map[string]any{"secretName": "auth"} }, false},
		{"none", func(v, _, _ map[string]any) { delete(v, "configMap") }, false},
		{"reserved", func(v, _, _ map[string]any) { v["name"] = "data" }, false},
		{"bad-name", func(v, _, _ map[string]any) { v["name"] = "UPPER" }, false},
		{"ref", func(_, m, _ map[string]any) { m["name"] = "missing" }, false},
		{"readonly-false", func(_, m, _ map[string]any) { m["readOnly"] = false }, false},
		{"readonly-missing", func(_, m, _ map[string]any) { delete(m, "readOnly") }, false},
		{"relative", func(_, m, _ map[string]any) { m["mountPath"] = "etc/input" }, false},
		{"unclean", func(_, m, _ map[string]any) { m["mountPath"] = "/etc/../input" }, false},
		{"root", func(_, m, _ map[string]any) { m["mountPath"] = "/" }, false},
		{"output", func(_, m, _ map[string]any) { m["mountPath"] = "/arprt/sub" }, false},
		{"context", func(_, m, j map[string]any) { j["contextPath"] = "/context/file"; m["mountPath"] = "/context" }, false},
		{"duplicate-volume", func(v, _, j map[string]any) { j["volumes"] = []any{v, v} }, false},
		{"duplicate-mount", func(_, m, j map[string]any) { j["renderer"].(map[string]any)["volumeMounts"] = []any{m, m} }, false},
		{"nested-mount", func(_, m, j map[string]any) {
			j["renderer"].(map[string]any)["volumeMounts"] = []any{m, map[string]any{"name": "config", "mountPath": "/etc/input/sub", "readOnly": true}}
		}, false},
	}
	for _, field := range []string{"subPath", "subPathExpr"} {
		cases = append(cases, fileAPICase{field + "-empty", func(_, m, _ map[string]any) { m[field] = "" }, false})
	}
	for _, field := range []string{"subPath", "subPathExpr", "mountPropagation", "recursiveReadOnly"} {
		cases = append(cases, fileAPICase{field, func(_, m, _ map[string]any) { m[field] = "value" }, false})
	}
	// Every known Kubernetes source is explicitly rejected, even alongside a valid source.
	typ := reflect.TypeFor[api.JobVolume]()
	for i := 1; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == "ConfigMap" || field.Name == "Secret" {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		cases = append(cases, fileAPICase{name, func(v, _, _ map[string]any) { v[name] = map[string]any{} }, false})
	}
	for _, p := range []string{"/absolute", "dir/../file", "..data/file", ".", "", "bad\x00file"} {
		cases = append(cases, fileAPICase{"item-path", func(v, _, _ map[string]any) {
			v["configMap"].(map[string]any)["items"].([]any)[0].(map[string]any)["path"] = p
		}, false})
	}

	for _, value := range []int64{-1, 512} {
		cases = append(cases, fileAPICase{"default-mode", func(v, _, _ map[string]any) { v["configMap"].(map[string]any)["defaultMode"] = value }, false})
	}
	for _, n := range []int{17} {
		cases = append(cases, fileAPICase{"volume-limit", func(v, _, j map[string]any) {
			vs := []any{}
			for k := 0; k < n; k++ {
				cp := volume()
				cp["name"] = fmt.Sprintf("v-%d", k)
				vs = append(vs, cp)
			}
			j["volumes"] = vs
		}, false})
	}
	cases = append(cases, fileAPICase{"item-limit", func(v, _, _ map[string]any) {
		items := []any{}
		for k := 0; k < 129; k++ {
			items = append(items, map[string]any{"key": "key", "path": fmt.Sprintf("file-%d", k)})
		}
		v["configMap"].(map[string]any)["items"] = items
	}, false})
	for i, tc := range cases {
		t.Run("files-"+tc.name, func(t *testing.T) {
			v, m := volume(), mount()
			j := map[string]any{"renderer": map[string]any{"image": "busybox:1.37.0", "volumeMounts": []any{m}}, "volumes": []any{v}}
			tc.change(v, m, j)
			o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": api.GroupVersion.String(), "kind": "ArprtScreen", "metadata": map[string]any{"name": fmt.Sprintf("files-%d", i), "namespace": "arprt"}, "spec": map[string]any{"source": map[string]any{"job": j}}}}
			err := c.Create(t.Context(), o)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if !apierrors.IsInvalid(err) {
				t.Fatalf("expected invalid input, got %v", err)
			}
		})
	}
}
