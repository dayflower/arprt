package byos

import (
	"net/http"
	"testing"
)

func TestPercentObservation(t *testing.T) {
	for _, value := range []string{"0", "19.999", "20", "59.999", "60", "100", "20.5"} {
		o := Observe(map[string]string{"percent-charged": value})
		if o.Percent == nil || len(o.Invalid) != 0 {
			t.Fatal("valid percentage rejected", value)
		}
	}
	for _, value := range []string{"", "NaN", "Inf", "-Inf", "-0.01", "100.01", "bad"} {
		o := Observe(map[string]string{"percent-charged": value})
		if o.Percent != nil || len(o.Invalid) != 1 {
			t.Fatal("invalid percentage accepted", value)
		}
	}
	h, err := Headers(http.Header{"PeRcEnT_ChArGeD": []string{"20.5"}})
	if err != nil || Observe(h).Percent == nil || *Observe(h).Percent != 20.5 {
		t.Fatal("alias failed")
	}
	h, err = Headers(http.Header{"Percent-Charged": []string{"20"}, "percent_charged": []string{"60"}})
	if err != nil || Observe(h).Percent != nil {
		t.Fatal("duplicate chose a percentage")
	}
	for _, name := range []string{"UTC", "Asia/Tokyo", "America/New_York", "Etc/GMT+9"} {
		if _, err := overlayLocation(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Local", "+09:00", "-09:00", "UTC+9", "unknown/zone"} {
		if _, err := overlayLocation(name); err == nil {
			t.Fatal("invalid time zone", name)
		}
	}
}
