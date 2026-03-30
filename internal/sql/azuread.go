package sql

import (
	"database/sql"
	"fmt"
	"net/url"
)

// NewAzureADClientFactory returns a factory that creates SQL clients using Azure AD auth.
// This uses the Azure AD token-based authentication flow with go-mssqldb.
func NewAzureADClientFactory() AzureADClientFactory {
	return func(host string, port int, clientID, tenantID, clientSecret string, tlsEnabled bool) (SQLClient, error) {
		query := url.Values{}
		query.Set("database", "master")
		query.Set("app name", "mssql-k8s-operator")
		query.Set("fedauth", "ActiveDirectoryServicePrincipal")

		encrypt := "disable"
		if tlsEnabled {
			encrypt = "true"
		}
		query.Set("encrypt", encrypt)

		u := &url.URL{
			Scheme:   "sqlserver",
			User:     url.UserPassword(clientID+"@"+tenantID, clientSecret),
			Host:     fmt.Sprintf("%s:%d", host, port),
			RawQuery: query.Encode(),
		}

		db, err := sql.Open("sqlserver", u.String())
		if err != nil {
			return nil, fmt.Errorf("failed to open Azure AD SQL connection: %w", err)
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		return &MSSQLClient{db: db}, nil
	}
}

// NewManagedIdentityClientFactory returns a factory that creates SQL clients using Azure Managed Identity.
func NewManagedIdentityClientFactory() ManagedIdentityClientFactory {
	return func(host string, port int, clientID string, tlsEnabled bool) (SQLClient, error) {
		query := url.Values{}
		query.Set("database", "master")
		query.Set("app name", "mssql-k8s-operator")
		query.Set("fedauth", "ActiveDirectoryManagedIdentity")

		if clientID != "" {
			query.Set("user id", clientID)
		}

		encrypt := "disable"
		if tlsEnabled {
			encrypt = "true"
		}
		query.Set("encrypt", encrypt)

		u := &url.URL{
			Scheme:   "sqlserver",
			Host:     fmt.Sprintf("%s:%d", host, port),
			RawQuery: query.Encode(),
		}

		db, err := sql.Open("sqlserver", u.String())
		if err != nil {
			return nil, fmt.Errorf("failed to open Managed Identity SQL connection: %w", err)
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		return &MSSQLClient{db: db}, nil
	}
}
