package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validBackup() *Backup {
	port := int32(1433)
	compression := false
	return &Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: BackupSpec{
			DatabaseName: "mydb",
			Destination:  "/backups/mydb.bak",
			Type:         BackupTypeFull,
			Compression:  &compression,
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

func TestBackupWebhook_Default_SetsPort(t *testing.T) {
	b := &Backup{
		Spec: BackupSpec{
			DatabaseName: "mydb",
			Destination:  "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	b.Default()

	if b.Spec.Server.Port == nil || *b.Spec.Server.Port != 1433 {
		t.Errorf("expected port=1433, got %v", b.Spec.Server.Port)
	}
}

func TestBackupWebhook_Default_SetsTypeFull(t *testing.T) {
	b := &Backup{
		Spec: BackupSpec{
			DatabaseName: "mydb",
			Destination:  "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	b.Default()

	if b.Spec.Type != BackupTypeFull {
		t.Errorf("expected type=Full, got %s", b.Spec.Type)
	}
}

func TestBackupWebhook_Default_SetsCompressionFalse(t *testing.T) {
	b := &Backup{
		Spec: BackupSpec{
			DatabaseName: "mydb",
			Destination:  "/backups/mydb.bak",
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	b.Default()

	if b.Spec.Compression == nil || *b.Spec.Compression != false {
		t.Error("expected compression=false")
	}
}

func TestBackupWebhook_Default_PreservesExistingType(t *testing.T) {
	b := &Backup{
		Spec: BackupSpec{
			DatabaseName: "mydb",
			Destination:  "/backups/mydb.bak",
			Type:         BackupTypeDifferential,
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	b.Default()

	if b.Spec.Type != BackupTypeDifferential {
		t.Errorf("expected type preserved as Differential, got %s", b.Spec.Type)
	}
}

// =============================================================================
// ValidateCreate
// =============================================================================

func TestBackupWebhook_ValidateCreate_Valid(t *testing.T) {
	b := validBackup()
	_, err := b.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestBackupWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	b := validBackup()
	b.Spec.DatabaseName = ""
	_, err := b.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing databaseName")
	}
}

func TestBackupWebhook_ValidateCreate_MissingDestination(t *testing.T) {
	b := validBackup()
	b.Spec.Destination = ""
	_, err := b.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing destination")
	}
}

func TestBackupWebhook_ValidateCreate_MissingHost(t *testing.T) {
	b := validBackup()
	b.Spec.Server.Host = ""
	_, err := b.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing host")
	}
}

func TestBackupWebhook_ValidateCreate_MissingCredentialsSecret(t *testing.T) {
	b := validBackup()
	b.Spec.Server.CredentialsSecret.Name = ""
	_, err := b.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing credentialsSecret")
	}
}

// =============================================================================
// ValidateUpdate — spec is fully immutable
// =============================================================================

func TestBackupWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validBackup()
	new := validBackup()
	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestBackupWebhook_ValidateUpdate_DatabaseNameChanged(t *testing.T) {
	old := validBackup()
	new := validBackup()
	new.Spec.DatabaseName = "otherdb"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed databaseName")
	}
}

func TestBackupWebhook_ValidateUpdate_DestinationChanged(t *testing.T) {
	old := validBackup()
	new := validBackup()
	new.Spec.Destination = "/backups/other.bak"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed destination")
	}
}

func TestBackupWebhook_ValidateUpdate_TypeChanged(t *testing.T) {
	old := validBackup()
	new := validBackup()
	new.Spec.Type = BackupTypeLog
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed type")
	}
}

func TestBackupWebhook_ValidateUpdate_HostChanged(t *testing.T) {
	old := validBackup()
	new := validBackup()
	new.Spec.Server.Host = "other.svc"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed host")
	}
}

// =============================================================================
// ValidateDelete — always OK
// =============================================================================

func TestBackupWebhook_ValidateDelete(t *testing.T) {
	b := validBackup()
	_, err := b.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
