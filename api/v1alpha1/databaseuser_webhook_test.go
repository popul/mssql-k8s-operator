package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDatabaseUserWebhook_Default_SetsPort(t *testing.T) {
	user := &DatabaseUser{
		Spec: DatabaseUserSpec{
			DatabaseName: "mydb",
			UserName:     "myuser",
			LoginRef:     LoginReference{Name: "mylogin"},
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	user.Default()

	if user.Spec.Server.Port == nil {
		t.Fatal("expected Port to be defaulted")
	}
	if *user.Spec.Server.Port != 1433 {
		t.Errorf("expected port 1433, got %d", *user.Spec.Server.Port)
	}
}

func TestDatabaseUserWebhook_ValidateCreate_Valid(t *testing.T) {
	user := validDatabaseUser()
	_, err := user.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestDatabaseUserWebhook_ValidateCreate_MissingUserName(t *testing.T) {
	user := validDatabaseUser()
	user.Spec.UserName = ""
	_, err := user.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty userName")
	}
}

func TestDatabaseUserWebhook_ValidateCreate_MissingDatabaseName(t *testing.T) {
	user := validDatabaseUser()
	user.Spec.DatabaseName = ""
	_, err := user.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty databaseName")
	}
}

func TestDatabaseUserWebhook_ValidateUpdate_UserNameImmutable(t *testing.T) {
	oldUser := validDatabaseUser()
	oldUser.Spec.UserName = "old_user"

	newUser := validDatabaseUser()
	newUser.Spec.UserName = "new_user"

	_, err := newUser.ValidateUpdate(oldUser)
	if err == nil {
		t.Error("expected error when changing userName")
	}
}

func TestDatabaseUserWebhook_ValidateUpdate_DatabaseNameImmutable(t *testing.T) {
	oldUser := validDatabaseUser()
	oldUser.Spec.DatabaseName = "old_db"

	newUser := validDatabaseUser()
	newUser.Spec.DatabaseName = "new_db"

	_, err := newUser.ValidateUpdate(oldUser)
	if err == nil {
		t.Error("expected error when changing databaseName")
	}
}

func TestDatabaseUserWebhook_ValidateCreate_MissingCredentialsSecretName(t *testing.T) {
	user := validDatabaseUser()
	user.Spec.Server.CredentialsSecret.Name = ""
	_, err := user.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty credentialsSecret name")
	}
}

func TestDatabaseUserWebhook_ValidateUpdate_PortImmutable(t *testing.T) {
	oldUser := validDatabaseUser()
	newPort := int32(1434)
	newUser := validDatabaseUser()
	newUser.Spec.Server.Port = &newPort

	_, err := newUser.ValidateUpdate(oldUser)
	if err == nil {
		t.Error("expected error when changing port")
	}
}

func TestDatabaseUserWebhook_ValidateUpdate_TLSImmutable(t *testing.T) {
	oldUser := validDatabaseUser()
	tls := true
	newUser := validDatabaseUser()
	newUser.Spec.Server.TLS = &tls

	_, err := newUser.ValidateUpdate(oldUser)
	if err == nil {
		t.Error("expected error when changing TLS")
	}
}

func TestDatabaseUserWebhook_ValidateUpdate_RolesCanChange(t *testing.T) {
	oldUser := validDatabaseUser()
	oldUser.Spec.DatabaseRoles = []string{"db_datareader"}

	newUser := validDatabaseUser()
	newUser.Spec.DatabaseRoles = []string{"db_datareader", "db_datawriter"}

	_, err := newUser.ValidateUpdate(oldUser)
	if err != nil {
		t.Errorf("expected roles to be mutable, got %v", err)
	}
}

func validDatabaseUser() *DatabaseUser {
	port := int32(1433)
	return &DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-user",
			Namespace: "default",
		},
		Spec: DatabaseUserSpec{
			DatabaseName: "mydb",
			UserName:     "myuser",
			LoginRef:     LoginReference{Name: "mylogin"},
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}
