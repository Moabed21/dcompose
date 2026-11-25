package views

import "testing"

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"web-1":          "web-1",
		"my proj/svc":    "my_proj_svc",
		`a:b*c?"d<e>|f\`: "a_b_c__d_e__f_",
		"..":             "logs",
		"":               "logs",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
