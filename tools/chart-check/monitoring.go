package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type monitorResource struct {
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		NamespaceSelector struct {
			MatchNames []string `json:"matchNames"`
		} `json:"namespaceSelector"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Endpoints []struct {
			Port, Scheme, Path, Interval string
			Relabelings                  []struct {
				SourceLabels []string `json:"sourceLabels"`
				TargetLabel  string   `json:"targetLabel"`
			} `json:"relabelings"`
		} `json:"podMetricsEndpoints"`
		Groups []struct {
			Name  string        `json:"name"`
			Rules []monitorRule `json:"rules"`
		} `json:"groups"`
	} `json:"spec"`
}
type monitorRule struct {
	Alert       string            `json:"alert"`
	Expr        string            `json:"expr"`
	For         string            `json:"for"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

func writeJSON(path string, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func checkMonitoring(helm, packages, promtool, dir string) error {
	values := filepath.Join(dir, "monitoring-values.json")
	metadata, err := readObjects("charts/arprt/Chart.yaml")
	if err != nil {
		return err
	}
	archive := filepath.Join(packages, "arprt-"+metadata[0]["version"].(string)+".tgz")
	// Copies of packaged charts are already checked above; also exercise monitoring
	// from the archive, independent of API discovery and local dependencies.
	for _, chart := range []string{"charts/arprt", archive} {
		for _, name := range []struct{ release, namespace string }{{"home", "custom-space"}, {"other", "custom-space"}, {"lab", "other-space"}} {
			for _, mode := range []struct{ pod, rules bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
				settings := object{"byos": object{"publicBaseURL": "https://byos.example.test/arprt"}, "metrics": object{
					"podMonitor":     object{"enabled": mode.pod, "interval": "45s", "additionalLabels": object{"monitor": "local"}},
					"prometheusRule": object{"enabled": mode.rules, "additionalLabels": object{"monitor": "local"}},
				}}
				if err := writeJSON(values, settings); err != nil {
					return err
				}
				objects, err := render(helm, chart, name.release, name.namespace, values)
				if err != nil {
					return err
				}
				for kind, enabled := range map[string]bool{"PodMonitor": mode.pod, "PrometheusRule": mode.rules} {
					count := 0
					for _, obj := range objects {
						if obj["kind"] == kind {
							count++
						}
					}
					want := 0
					if enabled {
						want = 1
					}
					if count != want {
						return fmt.Errorf("%s count=%d want=%d", kind, count, want)
					}
					if !enabled {
						continue
					}
					var resource monitorResource
					if err := one(objects, kind, &resource); err != nil {
						return err
					}
					if resource.APIVersion != "monitoring.coreos.com/v1" || resource.Metadata.Name != name.release+"-arprt" || resource.Metadata.Namespace != name.namespace || resource.Metadata.Labels["monitor"] != "local" || resource.Metadata.Labels["app.kubernetes.io/instance"] != name.release {
						return fmt.Errorf("%s identity/labels differ", kind)
					}
					if kind == "PodMonitor" {
						if !reflect.DeepEqual(resource.Spec.NamespaceSelector.MatchNames, []string{name.namespace}) || !reflect.DeepEqual(resource.Spec.Selector.MatchLabels, map[string]string{"app.kubernetes.io/name": "arprt", "app.kubernetes.io/instance": name.release}) || len(resource.Spec.Endpoints) != 1 {
							return fmt.Errorf("PodMonitor selector differs")
						}
						ep := resource.Spec.Endpoints[0]
						if ep.Port != "metrics" || ep.Path != "/metrics" || ep.Scheme != "http" || ep.Interval != "45s" || len(ep.Relabelings) != 2 || ep.Relabelings[0].TargetLabel != "namespace" || !reflect.DeepEqual(ep.Relabelings[0].SourceLabels, []string{"__meta_kubernetes_namespace"}) || ep.Relabelings[1].TargetLabel != "arprt_release" || !reflect.DeepEqual(ep.Relabelings[1].SourceLabels, []string{"__meta_kubernetes_pod_label_app_kubernetes_io_instance"}) {
							return fmt.Errorf("PodMonitor endpoint differs")
						}
					} else {
						if len(resource.Spec.Groups) != 1 || len(resource.Spec.Groups[0].Rules) != 3 {
							return fmt.Errorf("default enabled alerts differ")
						}
						for _, rule := range resource.Spec.Groups[0].Rules {
							if !strings.Contains(rule.Expr, fmt.Sprintf("namespace=%q", name.namespace)) || !strings.Contains(rule.Expr, fmt.Sprintf("arprt_release=%q", name.release)) {
								return fmt.Errorf("unscoped rule: %s", rule.Expr)
							}
						}
					}
				}
			}
		}
	}
	// Reject invalid combinations, malformed durations, thresholds and labels.
	invalid := []object{
		{"enabled": false, "podMonitor": object{"enabled": true}},
		{"enabled": false, "prometheusRule": object{"enabled": true}},
		{"podMonitor": object{"interval": "never"}},
		{"podMonitor": object{"interval": "0s"}},
		{"podMonitor": object{"additionalLabels": object{"app.kubernetes.io/instance": "other"}}},
		{"podMonitor": object{"additionalLabels": object{"bad key": "x"}}},
		{"podMonitor": object{"additionalLabels": object{strings.Repeat("a.", 127) + "a/name": "x"}}},
		{"podMonitor": object{"additionalLabels": object{"ok": "bad value"}}},
		{"prometheusRule": object{"labels": object{"bad/key": "x"}}},
		{"prometheusRule": object{"metricSelector": object{"bad-key": "x"}}},
		{"prometheusRule": object{"metricSelector": object{"result": "stale"}}},
		{"prometheusRule": object{"rules": object{"staleDeliveries": object{"threshold": 1.1}}}},
		{"prometheusRule": object{"rules": object{"staleDeliveries": object{"minimumDeliveries": 0}}}},
		{"prometheusRule": object{"rules": object{"storageErrors": object{"threshold": -1}}}},
		{"prometheusRule": object{"rules": object{"storageErrors": object{"window": "10"}}}},
		{"prometheusRule": object{"rules": object{"maintenanceStalled": object{"for": "bad"}}}},
	}
	for _, setting := range invalid {
		if err := writeJSON(values, object{"byos": object{"publicBaseURL": "https://byos.example.test/arprt"}, "metrics": setting}); err != nil {
			return err
		}
		_, err := render(helm, "charts/arprt", "home", "custom-space", values)
		if err == nil || !(strings.Contains(err.Error(), "metrics") || strings.Contains(err.Error(), "invalid propertyName")) {
			return fmt.Errorf("invalid monitoring settings accepted or unclear: %v: %v", setting, err)
		}
	}
	// Non-default rule settings must all reach the rendered rules.
	overrides := object{}
	for _, key := range []string{"maintenanceStalled", "storageErrors", "collectionFailures", "devicesSilent", "staleDeliveries"} {
		overrides[key] = object{"enabled": false}
	}
	settings := object{"byos": object{"publicBaseURL": "https://byos.example.test/arprt"}, "metrics": object{"prometheusRule": object{"enabled": true, "rules": overrides}}}
	if err := writeJSON(values, settings); err != nil {
		return err
	}
	objects, err := render(helm, "charts/arprt", "home", "custom-space", values)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if obj["kind"] == "PrometheusRule" {
			return fmt.Errorf("disabled rules generated a resource")
		}
	}
	overrides["storageErrors"] = object{"enabled": true, "threshold": 3, "window": "20m", "for": "2m"}
	ruleConfig := settings["metrics"].(object)["prometheusRule"].(object)
	ruleConfig["metricSelector"] = object{"job": "external\"\\\nsource"}
	ruleConfig["labels"] = object{"severity": "critical", "team": "display"}
	if err := writeJSON(values, settings); err != nil {
		return err
	}
	objects, err = render(helm, "charts/arprt", "home", "custom-space", values)
	if err != nil {
		return err
	}
	var custom monitorResource
	if err := one(objects, "PrometheusRule", &custom); err != nil {
		return err
	}
	rules := custom.Spec.Groups[0].Rules
	if len(rules) != 1 || rules[0].Expr != "increase(arprt_storage_errors_total{job=\"external\\\"\\\\\\nsource\",}[20m]) > 3" || rules[0].For != "2m" || rules[0].Labels["team"] != "display" || rules[0].Labels["severity"] != "critical" {
		return fmt.Errorf("custom rule differs: %+v", rules)
	}
	if err := checkRuleFile(promtool, dir, "custom", custom); err != nil {
		return err
	}
	// Exercise all default rules with the expressions emitted by Helm.
	if err := writeJSON(values, object{"byos": object{"publicBaseURL": "https://byos.example.test/arprt"}, "metrics": object{"prometheusRule": object{"enabled": true, "rules": object{"devicesSilent": object{"enabled": true}, "staleDeliveries": object{"enabled": true}}}}}); err != nil {
		return err
	}
	objects, err = render(helm, "charts/arprt", "home", "custom-space", values)
	if err != nil {
		return err
	}
	var all monitorResource
	if err := one(objects, "PrometheusRule", &all); err != nil {
		return err
	}
	if err := checkRuleFile(promtool, dir, "default", all); err != nil {
		return err
	}
	return testMonitoringRules(promtool, dir, all)
}

func checkRuleFile(promtool, dir, name string, resource monitorResource) error {
	path := filepath.Join(dir, name+"-rules.json")
	if err := writeJSON(path, object{"groups": resource.Spec.Groups}); err != nil {
		return err
	}
	_, err := command(promtool, "check", "rules", path)
	return err
}

// Expected alerts use the rendered rule's labels and annotations; expressions
// themselves always come from Helm, so template changes exercise real PromQL.
func testMonitoringRules(promtool, dir string, resource monitorResource) error {
	rules := map[string]monitorRule{}
	for _, group := range resource.Spec.Groups {
		for _, rule := range group.Rules {
			rules[rule.Alert] = rule
		}
	}
	labels := func(extra map[string]string) map[string]string {
		out := map[string]string{"namespace": "custom-space", "arprt_release": "home", "instance": "controller-a"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	series := func(metric, values string, extra map[string]string) object {
		ls := labels(extra)
		parts := []string{}
		for k, v := range ls {
			parts = append(parts, fmt.Sprintf("%s=%q", k, v))
		}
		// Map iteration order is irrelevant to PromQL series identity.
		return object{"series": metric + "{" + strings.Join(parts, ",") + "}", "values": values}
	}
	alert := func(name, at string, firing bool, extra map[string]string) object {
		expected := []any{}
		if firing {
			ls := labels(extra)
			for k, v := range rules[name].Labels {
				ls[k] = v
			}
			expected = append(expected, object{"exp_labels": ls, "exp_annotations": rules[name].Annotations})
		}
		return object{"alertname": name, "eval_time": at, "exp_alerts": expected}
	}
	tests := []any{}
	add := func(name string, input []any, assertions ...object) {
		tests = append(tests, object{"name": name, "interval": "1m", "input_series": input, "alert_rule_test": assertions})
	}
	maintenance := "ArprtMaintenanceStalled"
	add("maintenance threshold, grace, recovery and release/instance isolation", []any{
		series("arprt_maintenance_last_success_timestamp_seconds", "0+0x11 720+60x8", nil),
		series("arprt_maintenance_last_success_timestamp_seconds", "0+60x20", map[string]string{"instance": "controller-b"}),
		series("arprt_maintenance_last_success_timestamp_seconds", "0+0x20", map[string]string{"arprt_release": "other"}),
		series("arprt_maintenance_last_success_timestamp_seconds", "0+0x20", map[string]string{"namespace": "other-space"}),
	}, alert(maintenance, "5m", false, nil), alert(maintenance, "10m", false, nil), alert(maintenance, "11m", true, nil), alert(maintenance, "12m", false, nil))
	add("missing maintenance series cannot alert", nil, alert(maintenance, "20m", false, nil))
	add("maintenance scrape disappears", []any{series("arprt_maintenance_last_success_timestamp_seconds", "0+0x11 stale", nil)}, alert(maintenance, "11m", true, nil), alert(maintenance, "12m", false, nil))
	for _, pair := range []struct{ metric, name string }{{"arprt_storage_errors_total", "ArprtStorageErrors"}, {"arprt_collection_failures_total", "ArprtCollectionFailures"}} {
		extra := map[string]string{}
		if pair.name == "ArprtStorageErrors" {
			extra["operation"] = "read"
		}
		other := map[string]string{"arprt_release": "other"}
		for k, v := range extra {
			other[k] = v
		}
		add(pair.name+" increase and window recovery", []any{series(pair.metric, "0+0x4 1+0x15", extra), series(pair.metric, "0+10x20", other)},
			alert(pair.name, "4m", false, extra), alert(pair.name, "5m", true, extra), alert(pair.name, "13m", true, extra), alert(pair.name, "15m", false, extra))
		add(pair.name+" counter reset", []any{series(pair.metric, "0 1 2 0 1 2 3", extra)}, alert(pair.name, "3m", true, extra), alert(pair.name, "6m", true, extra))
		add(pair.name+" reset without errors", []any{series(pair.metric, "10+0x2 0+0x15", extra)}, alert(pair.name, "5m", false, extra))
	}
	silent := "ArprtDevicesSilent"
	add("silent threshold, hold and recovery", []any{series("arprt_devices", "0+0x4 1+0x14 0+0x10", map[string]string{"state": "silent"})}, alert(silent, "4m", false, map[string]string{"state": "silent"}), alert(silent, "14m", false, map[string]string{"state": "silent"}), alert(silent, "15m", true, map[string]string{"state": "silent"}), alert(silent, "20m", false, map[string]string{"state": "silent"}))
	stale := "ArprtStaleDeliveries"
	deliveries := func(ready, stale, unknown, pending string, instance string) []any {
		out := []any{}
		for key, value := range map[string]string{"ready": ready, "stale": stale, "unknown": unknown, "pending": pending} {
			out = append(out, series("arprt_deliveries_total", value, map[string]string{"result": key, "instance": instance}))
		}
		return out
	}
	add("stale ratio, minimum traffic, pending exclusion and instance isolation", append(deliveries("0+1x30", "0+3x30", "0+1x30", "0+100x30", "controller-a"), deliveries("0+100x30", "0+0x30", "0+0x30", "0+0x30", "controller-b")...), alert(stale, "1m", false, nil), alert(stale, "5m", true, nil))
	add("stale threshold equality and unknown denominator", deliveries("0+0x30", "0+1x30", "0+1x30", "0+0x30", "controller-a"), alert(stale, "15m", false, nil))
	add("stale low traffic", deliveries("0+0x30", "0+0.1x30", "0+0x30", "0+0x30", "controller-a"), alert(stale, "15m", false, nil))
	add("stale zero denominator", deliveries("0+0x30", "0+0x30", "0+0x30", "0+100x30", "controller-a"), alert(stale, "15m", false, nil))
	add("stale recovery", deliveries("0+1x5 20+20x20", "0+3x5 15+0x20", "0+0x30", "0+0x30", "controller-a"), alert(stale, "5m", true, nil), alert(stale, "20m", false, nil))
	add("stale counter reset", deliveries("0+1x3 0+1x20", "0+3x3 0+3x20", "0+0x30", "0+0x30", "controller-a"), alert(stale, "10m", true, nil))
	missing := []object{}
	for name := range rules {
		missing = append(missing, alert(name, "20m", false, nil))
	}
	add("all series absent", nil, missing...)
	path := filepath.Join(dir, "monitoring-tests.json")
	if err := writeJSON(path, object{"rule_files": []string{filepath.Join(dir, "default-rules.json")}, "evaluation_interval": "1m", "tests": tests}); err != nil {
		return err
	}
	_, err := command(promtool, "test", "rules", path)
	return err
}
