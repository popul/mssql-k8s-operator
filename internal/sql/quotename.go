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

// QuotePermissionTarget safely formats a permission target like "SCHEMA::app" into "SCHEMA::[app]".
// Supported formats: "SCHEMA::name", "OBJECT::schema.name", "DATABASE::name", "DATABASE".
func QuotePermissionTarget(target string) string {
	parts := strings.SplitN(target, "::", 2)
	if len(parts) == 1 {
		// Bare keyword like "DATABASE"
		return strings.ToUpper(parts[0])
	}
	scope := strings.ToUpper(parts[0])
	name := parts[1]

	// Handle OBJECT::schema.name
	if dotParts := strings.SplitN(name, ".", 2); len(dotParts) == 2 {
		return scope + "::" + QuoteName(dotParts[0]) + "." + QuoteName(dotParts[1])
	}
	return scope + "::" + QuoteName(name)
}

// IsValidPermission returns true if the given permission name is a known SQL Server permission.
func IsValidPermission(perm string) bool {
	valid := map[string]bool{
		"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
		"EXECUTE": true, "ALTER": true, "CONTROL": true, "REFERENCES": true,
		"VIEW DEFINITION": true, "CREATE TABLE": true, "CREATE VIEW": true,
		"CREATE PROCEDURE": true, "CREATE FUNCTION": true, "CREATE SCHEMA": true,
	}
	return valid[strings.ToUpper(perm)]
}

// QuoteString escapes a SQL Server string literal by doubling single quotes.
// Used for values in DDL statements where parameterized queries are not supported
// (e.g., CREATE LOGIN ... WITH PASSWORD = N'...').
func QuoteString(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "N'" + escaped + "'"
}
