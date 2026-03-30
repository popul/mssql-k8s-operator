package sql

import (
	"testing"
)

func TestMSSQLClient_ImplementsSQLClient(t *testing.T) {
	// Compile-time check that MSSQLClient implements SQLClient
	var _ SQLClient = (*MSSQLClient)(nil)
}

func TestBuildConnectionString_Basic(t *testing.T) {
	cs := buildConnectionString("myhost", 1433, "sa", "P@ssw0rd", false)
	if cs == "" {
		t.Fatal("expected non-empty connection string")
	}
	// Should contain host and port
	if !containsAll(cs, "myhost", "1433", "sa") {
		t.Errorf("connection string missing expected parts: %s", cs)
	}
}

func TestBuildConnectionString_TLS(t *testing.T) {
	csNoTLS := buildConnectionString("myhost", 1433, "sa", "P@ssw0rd", false)
	csTLS := buildConnectionString("myhost", 1433, "sa", "P@ssw0rd", true)

	if csNoTLS == csTLS {
		t.Error("TLS and non-TLS connection strings should differ")
	}
}

func TestBuildConnectionString_CustomPort(t *testing.T) {
	cs := buildConnectionString("myhost", 2433, "sa", "P@ssw0rd", false)
	if !containsAll(cs, "2433") {
		t.Errorf("expected port 2433 in connection string: %s", cs)
	}
}

func TestNewClientFactory_ReturnsFactory(t *testing.T) {
	factory := NewClientFactory()
	if factory == nil {
		t.Fatal("expected non-nil factory")
	}
}

// containsAll checks if s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
