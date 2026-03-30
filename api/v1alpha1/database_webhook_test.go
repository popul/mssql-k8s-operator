package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDatabaseWebhook_Default_SetsRetainPolicy(t *testing.T) {
	db := &Database{
		Spec: DatabaseSpec{
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	db.Default()

	if db.Spec.DeletionPolicy == nil {
		t.Fatal("expected DeletionPolicy to be defaulted")
	}
	if *db.Spec.DeletionPolicy != DeletionPolicyRetain {
		t.Errorf("expected Retain, got %s", *db.Spec.DeletionPolicy)
	}
}

func TestDatabaseWebhook_Default_PreservesExisting(t *testing.T) {
	policy := DeletionPolicyDelete
	db := &Database{
		Spec: DatabaseSpec{
			DatabaseName:   "mydb",
			DeletionPolicy: &policy,
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	db.Default()

	if *db.Spec.DeletionPolicy != DeletionPolicyDelete {
		t.Errorf("expected existing Delete policy preserved, got %s", *db.Spec.DeletionPolicy)
	}
}

func TestDatabaseWebhook_Default_SetsPort(t *testing.T) {
	db := &Database{
		Spec: DatabaseSpec{
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	db.Default()

	if db.Spec.Server.Port == nil {
		t.Fatal("expected Port to be defaulted")
	}
	if *db.Spec.Server.Port != 1433 {
		t.Errorf("expected port 1433, got %d", *db.Spec.Server.Port)
	}
}

func TestDatabaseWebhook_ValidateCreate_Valid(t *testing.T) {
	db := validDatabase()
	_, err := db.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestDatabaseWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	db := validDatabase()
	db.Spec.DatabaseName = ""
	_, err := db.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty databaseName")
	}
}

func TestDatabaseWebhook_ValidateCreate_MissingHost(t *testing.T) {
	db := validDatabase()
	db.Spec.Server.Host = ""
	_, err := db.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty host")
	}
}

func TestDatabaseWebhook_ValidateUpdate_CollationImmutable(t *testing.T) {
	oldCollation := "SQL_Latin1_General_CP1_CI_AS"
	newCollation := "Latin1_General_CI_AS"

	oldDB := validDatabase()
	oldDB.Spec.Collation = &oldCollation

	newDB := validDatabase()
	newDB.Spec.Collation = &newCollation

	_, err := newDB.ValidateUpdate(oldDB)
	if err == nil {
		t.Error("expected error when changing collation")
	}
}

func TestDatabaseWebhook_ValidateUpdate_CollationSameOK(t *testing.T) {
	collation := "SQL_Latin1_General_CP1_CI_AS"

	oldDB := validDatabase()
	oldDB.Spec.Collation = &collation

	newDB := validDatabase()
	newDB.Spec.Collation = &collation

	_, err := newDB.ValidateUpdate(oldDB)
	if err != nil {
		t.Errorf("expected no error when collation unchanged, got %v", err)
	}
}

func TestDatabaseWebhook_ValidateUpdate_DatabaseNameImmutable(t *testing.T) {
	oldDB := validDatabase()
	oldDB.Spec.DatabaseName = "old_name"

	newDB := validDatabase()
	newDB.Spec.DatabaseName = "new_name"

	_, err := newDB.ValidateUpdate(oldDB)
	if err == nil {
		t.Error("expected error when changing databaseName")
	}
}

func TestDatabaseWebhook_ValidateUpdate_ServerImmutable(t *testing.T) {
	oldDB := validDatabase()
	oldDB.Spec.Server.Host = "old-host"

	newDB := validDatabase()
	newDB.Spec.Server.Host = "new-host"

	_, err := newDB.ValidateUpdate(oldDB)
	if err == nil {
		t.Error("expected error when changing server host")
	}
}

func TestDatabaseWebhook_ValidateCreate_MissingCredentialsSecretName(t *testing.T) {
	db := validDatabase()
	db.Spec.Server.CredentialsSecret.Name = ""
	_, err := db.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty credentialsSecret name")
	}
}

func TestDatabaseWebhook_ValidateUpdate_PortImmutable(t *testing.T) {
	oldDB := validDatabase()
	newPort := int32(1434)
	newDB := validDatabase()
	newDB.Spec.Server.Port = &newPort

	_, err := newDB.ValidateUpdate(oldDB)
	if err == nil {
		t.Error("expected error when changing port")
	}
}

func TestDatabaseWebhook_ValidateUpdate_TLSImmutable(t *testing.T) {
	oldDB := validDatabase()
	tls := true
	newDB := validDatabase()
	newDB.Spec.Server.TLS = &tls

	_, err := newDB.ValidateUpdate(oldDB)
	if err == nil {
		t.Error("expected error when changing TLS")
	}
}

func TestDatabaseWebhook_ValidateDelete_Always(t *testing.T) {
	db := validDatabase()
	_, err := db.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error on delete, got %v", err)
	}
}

func validDatabase() *Database {
	port := int32(1433)
	return &Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-db",
			Namespace: "default",
		},
		Spec: DatabaseSpec{
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}

// Verify interface compliance
var _ context.Context // just to use the import if needed
