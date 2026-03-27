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

func TestQuoteNameIdempotenceIsNotExpected(t *testing.T) {
	// QuoteName is NOT idempotent by design - calling it twice wraps twice.
	// This documents the behavior.
	result := QuoteName(QuoteName("test"))
	expected := "[[test]]]"
	if result != expected {
		t.Errorf("QuoteName(QuoteName('test')) = %q, want %q", result, expected)
	}
}
