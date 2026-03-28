package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validPermission() *Permission {
	port := int32(1433)
	return &Permission{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-perm",
			Namespace: "default",
		},
		Spec: PermissionSpec{
			UserName:     "appuser",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
			Grants: []PermissionEntry{
				{Permission: "SELECT", On: "SCHEMA::app"},
			},
		},
	}
}

// =============================================================================
// Critère 25: Defaulting — port → 1433
// =============================================================================

func TestPermissionWebhook_Default_SetsPort(t *testing.T) {
	p := &Permission{
		Spec: PermissionSpec{
			UserName:     "appuser",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	p.Default()

	if p.Spec.Server.Port == nil {
		t.Fatal("expected Port to be defaulted")
	}
	if *p.Spec.Server.Port != 1433 {
		t.Errorf("expected port 1433, got %d", *p.Spec.Server.Port)
	}
}

func TestPermissionWebhook_Default_PreservesPort(t *testing.T) {
	port := int32(5000)
	p := &Permission{
		Spec: PermissionSpec{
			UserName:     "appuser",
			DatabaseName: "mydb",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	p.Default()

	if *p.Spec.Server.Port != 5000 {
		t.Errorf("expected port 5000 preserved, got %d", *p.Spec.Server.Port)
	}
}

// =============================================================================
// Critère 21: Champs requis
// =============================================================================

func TestPermissionWebhook_ValidateCreate_Valid(t *testing.T) {
	p := validPermission()
	_, err := p.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestPermissionWebhook_ValidateCreate_MissingUserName(t *testing.T) {
	p := validPermission()
	p.Spec.UserName = ""
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty userName")
	}
}

func TestPermissionWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	p := validPermission()
	p.Spec.DatabaseName = ""
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty databaseName")
	}
}

func TestPermissionWebhook_ValidateCreate_MissingHost(t *testing.T) {
	p := validPermission()
	p.Spec.Server.Host = ""
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty host")
	}
}

func TestPermissionWebhook_ValidateCreate_MissingCredentialsSecret(t *testing.T) {
	p := validPermission()
	p.Spec.Server.CredentialsSecret.Name = ""
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty credentialsSecret name")
	}
}

// =============================================================================
// Critère 22: Entries valides — permission et on non vides
// =============================================================================

func TestPermissionWebhook_ValidateCreate_EmptyGrantPermission(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = []PermissionEntry{
		{Permission: "", On: "SCHEMA::app"},
	}
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty grant permission")
	}
}

func TestPermissionWebhook_ValidateCreate_EmptyGrantOn(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = []PermissionEntry{
		{Permission: "SELECT", On: ""},
	}
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty grant on")
	}
}

func TestPermissionWebhook_ValidateCreate_EmptyDenyPermission(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = nil
	p.Spec.Denies = []PermissionEntry{
		{Permission: "", On: "SCHEMA::app"},
	}
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty deny permission")
	}
}

func TestPermissionWebhook_ValidateCreate_EmptyDenyOn(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = nil
	p.Spec.Denies = []PermissionEntry{
		{Permission: "DELETE", On: ""},
	}
	_, err := p.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty deny on")
	}
}

func TestPermissionWebhook_ValidateCreate_MultipleEntriesValid(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = []PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
		{Permission: "INSERT", On: "SCHEMA::app"},
	}
	p.Spec.Denies = []PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	_, err := p.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error for valid entries, got %v", err)
	}
}

func TestPermissionWebhook_ValidateCreate_NoEntries(t *testing.T) {
	p := validPermission()
	p.Spec.Grants = nil
	p.Spec.Denies = nil
	_, err := p.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error for empty grants/denies, got %v", err)
	}
}

// =============================================================================
// Critère 23: Immutabilité
// =============================================================================

func TestPermissionWebhook_ValidateUpdate_UserNameImmutable(t *testing.T) {
	old := validPermission()
	old.Spec.UserName = "old_user"
	new := validPermission()
	new.Spec.UserName = "new_user"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing userName")
	}
}

func TestPermissionWebhook_ValidateUpdate_DatabaseNameImmutable(t *testing.T) {
	old := validPermission()
	old.Spec.DatabaseName = "old_db"
	new := validPermission()
	new.Spec.DatabaseName = "new_db"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing databaseName")
	}
}

func TestPermissionWebhook_ValidateUpdate_HostImmutable(t *testing.T) {
	old := validPermission()
	new := validPermission()
	new.Spec.Server.Host = "other-host"

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing server host")
	}
}

func TestPermissionWebhook_ValidateUpdate_PortImmutable(t *testing.T) {
	old := validPermission()
	newPort := int32(1434)
	new := validPermission()
	new.Spec.Server.Port = &newPort

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing port")
	}
}

func TestPermissionWebhook_ValidateUpdate_TLSImmutable(t *testing.T) {
	old := validPermission()
	tls := true
	new := validPermission()
	new.Spec.Server.TLS = &tls

	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error when changing TLS")
	}
}

// =============================================================================
// Critère 24: Grants et Denies mutables
// =============================================================================

func TestPermissionWebhook_ValidateUpdate_GrantsMutable(t *testing.T) {
	old := validPermission()
	new := validPermission()
	new.Spec.Grants = []PermissionEntry{
		{Permission: "INSERT", On: "SCHEMA::app"},
		{Permission: "UPDATE", On: "SCHEMA::app"},
	}

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected grants to be mutable, got %v", err)
	}
}

func TestPermissionWebhook_ValidateUpdate_DeniesMutable(t *testing.T) {
	old := validPermission()
	new := validPermission()
	new.Spec.Denies = []PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected denies to be mutable, got %v", err)
	}
}

func TestPermissionWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validPermission()
	new := validPermission()

	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error for unchanged permission, got %v", err)
	}
}

func TestPermissionWebhook_ValidateDelete(t *testing.T) {
	p := validPermission()
	_, err := p.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error on delete, got %v", err)
	}
}
