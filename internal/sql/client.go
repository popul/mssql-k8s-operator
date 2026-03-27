package sql

import "fmt"

// NewClientFactory returns a ClientFactory that creates real SQL Server connections.
// TODO: Implement with go-mssqldb driver.
func NewClientFactory() ClientFactory {
	return func(host string, port int, username, password string, tlsEnabled bool) (SQLClient, error) {
		return nil, fmt.Errorf("real SQL client not yet implemented: connect to %s:%d", host, port)
	}
}
