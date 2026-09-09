package kubernetes

import (
	"strings"
	"testing"
)

func TestRedactBearerToken(t *testing.T) {
	in := []byte(`Authorization: Bearer abcdef1234567890.token.material`)
	out := string(RedactLogContent(in))
	if strings.Contains(out, "abcdef1234567890") {
		t.Errorf("bearer token must be redacted: %q", out)
	}
	if !strings.Contains(out, "Bearer [REDACTED]") {
		t.Errorf("prefix must survive: %q", out)
	}
}

func TestRedactJWT(t *testing.T) {
	in := []byte(`token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJVadQssw5c`)
	out := string(RedactLogContent(in))
	if strings.Contains(out, "eyJhbGciOi") {
		t.Errorf("jwt must be redacted: %q", out)
	}
}

func TestRedactPrivateKey(t *testing.T) {
	in := []byte("key:\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg==\n-----END PRIVATE KEY-----")
	out := string(RedactLogContent(in))
	if strings.Contains(out, "MIIEvQIB") {
		t.Errorf("pem private key must be redacted: %q", out)
	}
}

func TestRedactConnectionStringPassword(t *testing.T) {
	in := []byte(`postgres://user:s3cret@db.example.com/app?password=hunter2;server=x`)
	out := string(RedactLogContent(in))
	if strings.Contains(out, "hunter2") {
		t.Errorf("password must be redacted: %q", out)
	}
}

func TestRedactAWSKey(t *testing.T) {
	in := []byte(`aws_access_key_id=AKIAIOSFODNN7EXAMPLE`)
	out := string(RedactLogContent(in))
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("aws key must be redacted: %q", out)
	}
}

func TestRedactLeavesOrdinaryText(t *testing.T) {
	in := []byte(`level=info msg="payment settled" amount=100`)
	if got := string(RedactLogContent(in)); got != string(in) {
		t.Errorf("ordinary log lines must pass through: %q", got)
	}
}
