package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Terraform identifies itself in the User-Agent on every request the SDK makes,
// which is what lets objects it creates be marked without asking the operator to
// configure anything. The explicit header is the escape hatch and the way any
// other provisioner says who it is.
func TestDetectProvisioner(t *testing.T) {
	for _, c := range []struct {
		name      string
		userAgent string
		header    string
		want      string
	}{
		{"terraform sdk", "Terraform/1.9.8 (+https://www.terraform.io) terraform-provider-anomaly/0.3.1", "", "terraform"},
		{"explicit header wins", "curl/8.5.0", "Terraform", "terraform"},
		{"other provisioner", "python-requests/2.32", "pulumi", "pulumi"},
		{"a person in a browser", "Mozilla/5.0", "", ""},
		{"an ordinary api client", "curl/8.5.0", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
			r.Header.Set("User-Agent", c.userAgent)
			if c.header != "" {
				r.Header.Set(provisionerHeader, c.header)
			}
			if got := detectProvisioner(r); got != c.want {
				t.Errorf("detectProvisioner = %q, want %q", got, c.want)
			}
		})
	}
}
