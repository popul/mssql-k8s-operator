package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validAGFailover() *AGFailover {
	port := int32(1433)
	force := false
	return &AGFailover{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-failover",
			Namespace: "default",
		},
		Spec: AGFailoverSpec{
			AGName:        "myag",
			TargetReplica: "sql-1",
			Force:         &force,
			Server: ServerReference{
				Host:              "sql-1.sql-headless",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}

// =============================================================================
// Defaulting
// =============================================================================

func TestAGFailoverWebhook_Default_SetsForce(t *testing.T) {
	fo := &AGFailover{
		Spec: AGFailoverSpec{
			AGName:        "myag",
			TargetReplica: "sql-1",
			Server: ServerReference{
				Host:              "sql-1",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	fo.Default()

	if fo.Spec.Force == nil || *fo.Spec.Force != false {
		t.Error("expected force=false")
	}
}

func TestAGFailoverWebhook_Default_SetsPort(t *testing.T) {
	fo := &AGFailover{
		Spec: AGFailoverSpec{
			AGName:        "myag",
			TargetReplica: "sql-1",
			Server: ServerReference{
				Host:              "sql-1",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	fo.Default()

	if fo.Spec.Server.Port == nil || *fo.Spec.Server.Port != 1433 {
		t.Error("expected port=1433")
	}
}

// =============================================================================
// ValidateCreate
// =============================================================================

func TestAGFailoverWebhook_ValidateCreate_Valid(t *testing.T) {
	fo := validAGFailover()
	_, err := fo.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestAGFailoverWebhook_ValidateCreate_MissingAGName(t *testing.T) {
	fo := validAGFailover()
	fo.Spec.AGName = ""
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing agName")
	}
}

func TestAGFailoverWebhook_ValidateCreate_MissingTarget(t *testing.T) {
	fo := validAGFailover()
	fo.Spec.TargetReplica = ""
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing targetReplica")
	}
}

func TestAGFailoverWebhook_ValidateCreate_MissingHost(t *testing.T) {
	fo := validAGFailover()
	fo.Spec.Server.Host = ""
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing host")
	}
}

func TestAGFailoverWebhook_ValidateCreate_MissingCredentials(t *testing.T) {
	fo := validAGFailover()
	fo.Spec.Server.CredentialsSecret.Name = ""
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing credentialsSecret")
	}
}

func TestAGFailoverWebhook_ValidateCreate_ForceWithoutAnnotation(t *testing.T) {
	fo := validAGFailover()
	force := true
	fo.Spec.Force = &force
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for force=true without confirm-data-loss annotation")
	}
}

func TestAGFailoverWebhook_ValidateCreate_ForceWithAnnotation(t *testing.T) {
	fo := validAGFailover()
	force := true
	fo.Spec.Force = &force
	fo.Annotations = map[string]string{
		ConfirmDataLossAnnotation: "yes",
	}
	_, err := fo.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error with force + annotation, got %v", err)
	}
}

func TestAGFailoverWebhook_ValidateCreate_ForceWithWrongAnnotation(t *testing.T) {
	fo := validAGFailover()
	force := true
	fo.Spec.Force = &force
	fo.Annotations = map[string]string{
		ConfirmDataLossAnnotation: "no",
	}
	_, err := fo.ValidateCreate()
	if err == nil {
		t.Error("expected error for force=true with annotation != 'yes'")
	}
}

// =============================================================================
// ValidateUpdate — fully immutable
// =============================================================================

func TestAGFailoverWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validAGFailover()
	new := validAGFailover()
	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestAGFailoverWebhook_ValidateUpdate_AGNameChanged(t *testing.T) {
	old := validAGFailover()
	new := validAGFailover()
	new.Spec.AGName = "otherag"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed agName")
	}
}

func TestAGFailoverWebhook_ValidateUpdate_TargetChanged(t *testing.T) {
	old := validAGFailover()
	new := validAGFailover()
	new.Spec.TargetReplica = "sql-2"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed targetReplica")
	}
}

func TestAGFailoverWebhook_ValidateUpdate_HostChanged(t *testing.T) {
	old := validAGFailover()
	new := validAGFailover()
	new.Spec.Server.Host = "other.svc"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed host")
	}
}

func TestAGFailoverWebhook_ValidateUpdate_ForceChanged(t *testing.T) {
	old := validAGFailover()
	new := validAGFailover()
	force := true
	new.Spec.Force = &force
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed force")
	}
}

// =============================================================================
// ValidateDelete
// =============================================================================

func TestAGFailoverWebhook_ValidateDelete(t *testing.T) {
	fo := validAGFailover()
	_, err := fo.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
