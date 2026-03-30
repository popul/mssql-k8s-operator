package sql

import "testing"

func TestQuoteName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple name", "mydb", "[mydb]"},
		{"name with space", "my db", "[my db]"},
		{"name with closing bracket", "test]db", "[test]]db]"},
		{"name with opening bracket", "test[db", "[test[db]"},
		{"name with both brackets", "test[x]db", "[test[x]]db]"},
		{"name with double closing bracket", "test]]db", "[test]]]]db]"},
		{"empty string", "", "[]"},
		{"sql injection attempt", "'; DROP DATABASE master; --", "['; DROP DATABASE master; --]"},
		{"name with quotes", `test"db`, `[test"db]`},
		{"unicode name", "données", "[données]"},
		{"very long name", "a" + string(make([]byte, 128)), "[a" + string(make([]byte, 128)) + "]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := QuoteName(tt.input)
			if result != tt.expected {
				t.Errorf("QuoteName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestQuoteString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple password", "P@ssw0rd", "N'P@ssw0rd'"},
		{"password with single quote", "it's", "N'it''s'"},
		{"password with multiple quotes", "a'b'c", "N'a''b''c'"},
		{"empty string", "", "N''"},
		{"sql injection attempt", "'; DROP DATABASE master; --", "N'''; DROP DATABASE master; --'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := QuoteString(tt.input)
			if result != tt.expected {
				t.Errorf("QuoteString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Critère 27-30: QuotePermissionTarget
// =============================================================================

func TestQuotePermissionTarget(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Critère 27: Simple scope
		{"schema scope", "SCHEMA::app", "SCHEMA::[app]"},
		{"schema lowercase", "schema::app", "SCHEMA::[app]"},
		// Critère 28: Object with schema.name
		{"object with schema", "OBJECT::dbo.Users", "OBJECT::[dbo].[Users]"},
		{"object lowercase", "object::sales.orders", "OBJECT::[sales].[orders]"},
		// Critère 29: Bare keyword
		{"bare DATABASE", "DATABASE", "DATABASE"},
		{"bare database lowercase", "database", "DATABASE"},
		// Critère 30: Injection via brackets
		{"bracket injection", "SCHEMA::test]name", "SCHEMA::[test]]name]"},
		{"double bracket", "SCHEMA::a]]b", "SCHEMA::[a]]]]b]"},
		// Edge cases
		{"database scope with name", "DATABASE::mydb", "DATABASE::[mydb]"},
		{"object with bracket in schema", "OBJECT::dbo].tbl", "OBJECT::[dbo]]].[tbl]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := QuotePermissionTarget(tt.input)
			if result != tt.expected {
				t.Errorf("QuotePermissionTarget(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Critère 31-33: IsValidPermission
// =============================================================================

func TestIsValidPermission(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Critère 31: Valid permissions
		{"SELECT", "SELECT", true},
		{"INSERT", "INSERT", true},
		{"UPDATE", "UPDATE", true},
		{"DELETE", "DELETE", true},
		{"EXECUTE", "EXECUTE", true},
		{"ALTER", "ALTER", true},
		{"CONTROL", "CONTROL", true},
		{"REFERENCES", "REFERENCES", true},
		{"VIEW DEFINITION", "VIEW DEFINITION", true},
		{"CREATE TABLE", "CREATE TABLE", true},
		{"CREATE VIEW", "CREATE VIEW", true},
		{"CREATE PROCEDURE", "CREATE PROCEDURE", true},
		{"CREATE FUNCTION", "CREATE FUNCTION", true},
		{"CREATE SCHEMA", "CREATE SCHEMA", true},
		// Critère 32: Invalid permissions
		{"DROP", "DROP", false},
		{"INJECT", "INJECT", false},
		{"empty", "", false},
		{"sql injection", "SELECT; DROP TABLE", false},
		{"random text", "foobar", false},
		// Critère 33: Case insensitive
		{"lowercase select", "select", true},
		{"mixed case Select", "Select", true},
		{"lowercase insert", "insert", true},
		{"mixed view definition", "View Definition", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidPermission(tt.input)
			if result != tt.expected {
				t.Errorf("IsValidPermission(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestQuoteNameIdempotenceIsNotExpected(t *testing.T) {
	// QuoteName is NOT idempotent by design - calling it twice wraps twice.
	// This documents the behavior.
	result := QuoteName(QuoteName("test"))
	expected := "[[test]]]"
	if result != expected {
		t.Errorf("QuoteName(QuoteName('test')) = %q, want %q", result, expected)
	}
}
