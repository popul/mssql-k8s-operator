package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validRestore() *Restore {
	port := int32(1433)
	return &Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: RestoreSpec{
			DatabaseName: "mydb",
			Source:       "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}

// =============================================================================
// Defaulting
// =============================================================================

func TestRestoreWebhook_Default_SetsPort(t *testing.T) {
	r := &Restore{
		Spec: RestoreSpec{
			DatabaseName: "mydb",
			Source:       "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	r.Default()

	if r.Spec.Server.Port == nil || *r.Spec.Server.Port != 1433 {
		t.Errorf("expected port=1433, got %v", r.Spec.Server.Port)
	}
}

func TestRestoreWebhook_Default_PreservesExistingPort(t *testing.T) {
	port := int32(5000)
	r := &Restore{
		Spec: RestoreSpec{
			DatabaseName: "mydb",
			Source:       "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	r.Default()

	if *r.Spec.Server.Port != 5000 {
		t.Errorf("expected port=5000 preserved, got %d", *r.Spec.Server.Port)
	}
}

// =============================================================================
// ValidateCreate
// =============================================================================

func TestRestoreWebhook_ValidateCreate_Valid(t *testing.T) {
	r := validRestore()
	_, err := r.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestRestoreWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	r := validRestore()
	r.Spec.DatabaseName = ""
	_, err := r.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing databaseName")
	}
}

func TestRestoreWebhook_ValidateCreate_MissingSource(t *testing.T) {
	r := validRestore()
	r.Spec.Source = ""
	_, err := r.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestRestoreWebhook_ValidateCreate_MissingHost(t *testing.T) {
	r := validRestore()
	r.Spec.Server.Host = ""
	_, err := r.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing host")
	}
}

func TestRestoreWebhook_ValidateCreate_MissingCredentialsSecret(t *testing.T) {
	r := validRestore()
	r.Spec.Server.CredentialsSecret.Name = ""
	_, err := r.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing credentialsSecret")
	}
}

// =============================================================================
// ValidateUpdate — spec is fully immutable
// =============================================================================

func TestRestoreWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validRestore()
	new := validRestore()
	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestRestoreWebhook_ValidateUpdate_DatabaseNameChanged(t *testing.T) {
	old := validRestore()
	new := validRestore()
	new.Spec.DatabaseName = "otherdb"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed databaseName")
	}
}

func TestRestoreWebhook_ValidateUpdate_SourceChanged(t *testing.T) {
	old := validRestore()
	new := validRestore()
	new.Spec.Source = "/backups/other.bak"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed source")
	}
}

func TestRestoreWebhook_ValidateUpdate_HostChanged(t *testing.T) {
	old := validRestore()
	new := validRestore()
	new.Spec.Server.Host = "other.svc"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed host")
	}
}

// =============================================================================
// ValidateDelete — always OK
// =============================================================================

func TestRestoreWebhook_ValidateDelete(t *testing.T) {
	r := validRestore()
	_, err := r.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
