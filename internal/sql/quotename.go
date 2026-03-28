package sql

import "strings"

// QuoteName escapes a SQL Server identifier by wrapping it in square brackets
// and escaping any embedded closing brackets. This is equivalent to T-SQL's QUOTENAME().
//
// This MUST be used for all dynamic identifiers in DDL statements (database names,
// login names, user names, role names) since DDL does not support parameterized identifiers.
func QuoteName(name string) string {
	escaped := strings.ReplaceAll(name, "]", "]]")
	return "[" + escaped + "]"
}

// QuoteString escapes a SQL Server string literal by doubling single quotes.
// Used for values in DDL statements where parameterized queries are not supported
// (e.g., CREATE LOGIN ... WITH PASSWORD = N'...').
func QuoteString(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "N'" + escaped + "'"
}
