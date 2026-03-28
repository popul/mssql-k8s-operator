package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validAG() *AvailabilityGroup {
	port := int32(1433)
	return &AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ag",
			Namespace: "default",
		},
		Spec: AvailabilityGroupSpec{
			AGName: "myag",
			Replicas: []AGReplicaSpec{
				{
					ServerName:       "sql-0",
					EndpointURL:      "TCP://sql-0.sql-headless:5022",
					AvailabilityMode: AvailabilityModeSynchronous,
					FailoverMode:     FailoverModeAutomatic,
					SeedingMode:      SeedingModeAutomatic,
					SecondaryRole:    SecondaryRoleNo,
					Server: ServerReference{
						Host:              "sql-0.sql-headless",
						Port:              &port,
						CredentialsSecret: SecretReference{Name: "sa-credentials"},
					},
				},
				{
					ServerName:       "sql-1",
					EndpointURL:      "TCP://sql-1.sql-headless:5022",
					AvailabilityMode: AvailabilityModeSynchronous,
					FailoverMode:     FailoverModeAutomatic,
					SeedingMode:      SeedingModeAutomatic,
					SecondaryRole:    SecondaryRoleNo,
					Server: ServerReference{
						Host:              "sql-1.sql-headless",
						Port:              &port,
						CredentialsSecret: SecretReference{Name: "sa-credentials"},
					},
				},
			},
		},
	}
}

// =============================================================================
// Defaulting
// =============================================================================

func TestAGWebhook_Default_SetsBackupPreference(t *testing.T) {
	ag := &AvailabilityGroup{
		Spec: AvailabilityGroupSpec{
			AGName: "myag",
			Replicas: []AGReplicaSpec{
				{ServerName: "sql-0", EndpointURL: "TCP://sql-0:5022", Server: ServerReference{Host: "sql-0", CredentialsSecret: SecretReference{Name: "sa"}}},
				{ServerName: "sql-1", EndpointURL: "TCP://sql-1:5022", Server: ServerReference{Host: "sql-1", CredentialsSecret: SecretReference{Name: "sa"}}},
			},
		},
	}
	ag.Default()

	if ag.Spec.AutomatedBackupPreference == nil || *ag.Spec.AutomatedBackupPreference != "Secondary" {
		t.Error("expected AutomatedBackupPreference=Secondary")
	}
}

func TestAGWebhook_Default_SetsDBFailover(t *testing.T) {
	ag := &AvailabilityGroup{
		Spec: AvailabilityGroupSpec{
			AGName: "myag",
			Replicas: []AGReplicaSpec{
				{ServerName: "sql-0", EndpointURL: "TCP://sql-0:5022", Server: ServerReference{Host: "sql-0", CredentialsSecret: SecretReference{Name: "sa"}}},
				{ServerName: "sql-1", EndpointURL: "TCP://sql-1:5022", Server: ServerReference{Host: "sql-1", CredentialsSecret: SecretReference{Name: "sa"}}},
			},
		},
	}
	ag.Default()

	if ag.Spec.DBFailover == nil || *ag.Spec.DBFailover != true {
		t.Error("expected DBFailover=true")
	}
}

func TestAGWebhook_Default_SetsReplicaDefaults(t *testing.T) {
	ag := &AvailabilityGroup{
		Spec: AvailabilityGroupSpec{
			AGName: "myag",
			Replicas: []AGReplicaSpec{
				{ServerName: "sql-0", EndpointURL: "TCP://sql-0:5022", Server: ServerReference{Host: "sql-0", CredentialsSecret: SecretReference{Name: "sa"}}},
				{ServerName: "sql-1", EndpointURL: "TCP://sql-1:5022", Server: ServerReference{Host: "sql-1", CredentialsSecret: SecretReference{Name: "sa"}}},
			},
		},
	}
	ag.Default()

	for i, r := range ag.Spec.Replicas {
		if r.AvailabilityMode != AvailabilityModeSynchronous {
			t.Errorf("replica[%d]: expected AvailabilityMode=SynchronousCommit", i)
		}
		if r.FailoverMode != FailoverModeAutomatic {
			t.Errorf("replica[%d]: expected FailoverMode=Automatic", i)
		}
		if r.SeedingMode != SeedingModeAutomatic {
			t.Errorf("replica[%d]: expected SeedingMode=Automatic", i)
		}
		if r.Server.Port == nil || *r.Server.Port != 1433 {
			t.Errorf("replica[%d]: expected port=1433", i)
		}
	}
}

// =============================================================================
// ValidateCreate
// =============================================================================

func TestAGWebhook_ValidateCreate_Valid(t *testing.T) {
	ag := validAG()
	_, err := ag.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestAGWebhook_ValidateCreate_MissingAGName(t *testing.T) {
	ag := validAG()
	ag.Spec.AGName = ""
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing agName")
	}
}

func TestAGWebhook_ValidateCreate_OnlyOneReplica(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas = ag.Spec.Replicas[:1]
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for less than 2 replicas")
	}
}

func TestAGWebhook_ValidateCreate_DuplicateServerName(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[1].ServerName = ag.Spec.Replicas[0].ServerName
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for duplicate serverName")
	}
}

func TestAGWebhook_ValidateCreate_MissingEndpointURL(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[0].EndpointURL = ""
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing endpointURL")
	}
}

func TestAGWebhook_ValidateCreate_MissingReplicaHost(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[0].Server.Host = ""
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing replica host")
	}
}

func TestAGWebhook_ValidateCreate_MissingReplicaCredentials(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[0].Server.CredentialsSecret.Name = ""
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for missing replica credentialsSecret")
	}
}

func TestAGWebhook_ValidateCreate_AutoFailoverWithAsyncCommit(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[1].AvailabilityMode = AvailabilityModeAsynchronous
	ag.Spec.Replicas[1].FailoverMode = FailoverModeAutomatic
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for automatic failover with asynchronous commit")
	}
}

func TestAGWebhook_ValidateCreate_ManualFailoverWithAsyncCommit(t *testing.T) {
	ag := validAG()
	ag.Spec.Replicas[1].AvailabilityMode = AvailabilityModeAsynchronous
	ag.Spec.Replicas[1].FailoverMode = FailoverModeManual
	_, err := ag.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error for manual failover with async commit, got %v", err)
	}
}

func TestAGWebhook_ValidateCreate_DuplicateDatabase(t *testing.T) {
	ag := validAG()
	ag.Spec.Databases = []AGDatabaseSpec{{Name: "db1"}, {Name: "db1"}}
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for duplicate database name")
	}
}

func TestAGWebhook_ValidateCreate_EmptyListenerName(t *testing.T) {
	ag := validAG()
	ag.Spec.Listener = &AGListenerSpec{Name: ""}
	_, err := ag.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty listener name")
	}
}

// =============================================================================
// ValidateUpdate — agName is immutable
// =============================================================================

func TestAGWebhook_ValidateUpdate_NoChange(t *testing.T) {
	old := validAG()
	new := validAG()
	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestAGWebhook_ValidateUpdate_AGNameChanged(t *testing.T) {
	old := validAG()
	new := validAG()
	new.Spec.AGName = "otherag"
	_, err := new.ValidateUpdate(old)
	if err == nil {
		t.Error("expected error for changed agName")
	}
}

func TestAGWebhook_ValidateUpdate_ReplicasCanChange(t *testing.T) {
	old := validAG()
	new := validAG()
	port := int32(1433)
	new.Spec.Replicas = append(new.Spec.Replicas, AGReplicaSpec{
		ServerName:  "sql-2",
		EndpointURL: "TCP://sql-2:5022",
		Server:      ServerReference{Host: "sql-2", Port: &port, CredentialsSecret: SecretReference{Name: "sa-credentials"}},
	})
	_, err := new.ValidateUpdate(old)
	if err != nil {
		t.Errorf("expected replicas to be mutable, got %v", err)
	}
}

// =============================================================================
// ValidateDelete
// =============================================================================

func TestAGWebhook_ValidateDelete(t *testing.T) {
	ag := validAG()
	_, err := ag.ValidateDelete()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
