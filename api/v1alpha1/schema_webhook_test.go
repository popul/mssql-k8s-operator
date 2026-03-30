package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validSchema() *Schema {
	port := int32(1433)
	return &Schema{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schema",
			Namespace: "default",
		},
		Spec: SchemaSpec{
			SchemaName:   "app",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}

// =============================================================================
// Critère 23: Defaulting — deletionPolicy → Retain, port → 1433
// =============================================================================

func TestSchemaWebhook_Default_SetsRetainPolicy(t *testing.T) {
	s := &Schema{
		Spec: SchemaSpec{
			SchemaName:   "app",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	s.Default()

	if s.Spec.DeletionPolicy == nil {
		t.Fatal("expected DeletionPolicy to be defaulted")
	}
	if *s.Spec.DeletionPolicy != DeletionPolicyRetain {
		t.Errorf("expected Retain, got %s", *s.Spec.DeletionPolicy)
	}
}

func TestSchemaWebhook_Default_PreservesExistingPolicy(t *testing.T) {
	policy := DeletionPolicyDelete
	s := &Schema{
		Spec: SchemaSpec{
			SchemaName:     "app",
			DatabaseName:   "mydb",
			DeletionPolicy: &policy,
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	s.Default()

	if *s.Spec.DeletionPolicy != DeletionPolicyDelete {
		t.Errorf("expected existing Delete policy preserved, got %s", *s.Spec.DeletionPolicy)
	}
}

func TestSchemaWebhook_Default_SetsPort(t *testing.T) {
	s := &Schema{
		Spec: SchemaSpec{
			SchemaName:   "app",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	s.Default()

	if s.Spec.Server.Port == nil {
		t.Fatal("expected Port to be defaulted")
	}
	if *s.Spec.Server.Port != 1433 {
		t.Errorf("expected port 1433, got %d", *s.Spec.Server.Port)
	}
}

// =============================================================================
// Critère 20: Champs requis
// =============================================================================

func TestSchemaWebhook_ValidateCreate_Valid(t *testing.T) {
	s := validSchema()
	_, err := s.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestSchemaWebhook_ValidateCreate_MissingSchemaName(t *testing.T) {
	s := validSchema()
	s.Spec.SchemaName = ""
	_, err := s.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty schemaName")
	}
}

func TestSchemaWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	s := validSchema()
	s.Spec.DatabaseName = ""
	_, err := s.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty databaseName")
	}
}

func TestSchemaWebhook_ValidateCreate_MissingHost(t *testing.T) {
	s := validSchema()
	s.Spec.Server.Host = ""
	_, err := s.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty host")
	}
}

func TestSchemaWebhook_ValidateCreate_MissingCredentialsSecret(t *testing.T) {
	s := validSchema()
	s.Spec.Server.CredentialsSecret.Name = ""
	_, err := s.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty credentialsSecret name")
	}
}

// =============================================================================
// Critère 21: Immutabilité
// =============================================================================

func TestSchemaWebhook_ValidateUpdate_SchemaNameImmutable(t *testing.T) {
	old := validSchema()
	old.Spec.SchemaName = "old_schema"
	new := validSchema()
	new.Spec.SchemaName = "new_schema"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing schemaName")
	}
}

func TestSchemaWebhook_ValidateUpdate_DatabaseNameImmutable(t *testing.T) {
	old := validSchema()
	old.Spec.DatabaseName = "old_db"
	new := validSchema()
	new.Spec.DatabaseName = "new_db"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing databaseName")
	}
}

func TestSchemaWebhook_ValidateUpdate_HostImmutable(t *testing.T) {
	old := validSchema()
	new := validSchema()
	new.Spec.Server.Host = "other-host"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing server host")
	}
}

func TestSchemaWebhook_ValidateUpdate_PortImmutable(t *testing.T) {
	old := validSchema()
	newPort := int32(1434)
	new := validSchema()
	new.Spec.Server.Port = &newPort

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing port")
	}
}

func TestSchemaWebhook_ValidateUpdate_TLSImmutable(t *testing.T) {
	old := validSchema()
	tls := true
	new := validSchema()
	new.Spec.Server.TLS = &tls

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing TLS")
	}
}

// =============================================================================
// Critère 22: Owner et deletionPolicy mutables
// =============================================================================

func TestSchemaWebhook_ValidateUpdate_OwnerMutable(t *testing.T) {
	old := validSchema()
	owner := "newowner"
	new := validSchema()
	new.Spec.Owner = &owner

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected owner to be mutable, got %v", err)
	}
}

func TestSchemaWebhook_ValidateUpdate_DeletionPolicyMutable(t *testing.T) {
	old := validSchema()
	policy := DeletionPolicyDelete
	new := validSchema()
	new.Spec.DeletionPolicy = &policy

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected deletionPolicy to be mutable, got %v", err)
	}
}

func TestSchemaWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validSchema()
	new := validSchema()

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error for unchanged schema, got %v", err)
	}
}

func TestSchemaWebhook_ValidateDelete(t *testing.T) {
	s := validSchema()
	_, err := s.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error on delete, got %v", err)
	}
}
