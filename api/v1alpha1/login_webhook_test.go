package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoginWebhook_Default_SetsRetainPolicy(t *testing.T) {
	login := &Login{
		Spec: LoginSpec{
			LoginName:      "mylogin",
			PasswordSecret: SecretReference{Name: "pass"},
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	login.Default()

	if login.Spec.DeletionPolicy == nil {
		t.Fatal("expected DeletionPolicy to be defaulted")
	}
	if *login.Spec.DeletionPolicy != DeletionPolicyRetain {
		t.Errorf("expected Retain, got %s", *login.Spec.DeletionPolicy)
	}
}

func TestLoginWebhook_Default_SetsPort(t *testing.T) {
	login := &Login{
		Spec: LoginSpec{
			LoginName:      "mylogin",
			PasswordSecret: SecretReference{Name: "pass"},
			Server: ServerReference{
				Host:              "mssql.svc",
				CredentialsSecret: SecretReference{Name: "sa"},
			},
		},
	}
	login.Default()

	if login.Spec.Server.Port == nil {
		t.Fatal("expected Port to be defaulted")
	}
	if *login.Spec.Server.Port != 1433 {
		t.Errorf("expected port 1433, got %d", *login.Spec.Server.Port)
	}
}

func TestLoginWebhook_ValidateCreate_Valid(t *testing.T) {
	login := validLogin()
	_, err := login.ValidateCreate()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestLoginWebhook_ValidateCreate_MissingLoginName(t *testing.T) {
	login := validLogin()
	login.Spec.LoginName = ""
	_, err := login.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty loginName")
	}
}

func TestLoginWebhook_ValidateUpdate_LoginNameImmutable(t *testing.T) {
	oldLogin := validLogin()
	oldLogin.Spec.LoginName = "old_login"

	newLogin := validLogin()
	newLogin.Spec.LoginName = "new_login"

	_, err := newLogin.ValidateUpdate(oldLogin)
	if err == nil {
		t.Error("expected error when changing loginName")
	}
}

func TestLoginWebhook_ValidateUpdate_ServerImmutable(t *testing.T) {
	oldLogin := validLogin()
	oldLogin.Spec.Server.Host = "old-host"

	newLogin := validLogin()
	newLogin.Spec.Server.Host = "new-host"

	_, err := newLogin.ValidateUpdate(oldLogin)
	if err == nil {
		t.Error("expected error when changing server host")
	}
}

func TestLoginWebhook_ValidateCreate_MissingCredentialsSecretName(t *testing.T) {
	login := validLogin()
	login.Spec.Server.CredentialsSecret.Name = ""
	_, err := login.ValidateCreate()
	if err == nil {
		t.Error("expected error for empty credentialsSecret name")
	}
}

func TestLoginWebhook_ValidateUpdate_PortImmutable(t *testing.T) {
	oldLogin := validLogin()
	newPort := int32(1434)
	newLogin := validLogin()
	newLogin.Spec.Server.Port = &newPort

	_, err := newLogin.ValidateUpdate(oldLogin)
	if err == nil {
		t.Error("expected error when changing port")
	}
}

func TestLoginWebhook_ValidateUpdate_TLSImmutable(t *testing.T) {
	oldLogin := validLogin()
	tls := true
	newLogin := validLogin()
	newLogin.Spec.Server.TLS = &tls

	_, err := newLogin.ValidateUpdate(oldLogin)
	if err == nil {
		t.Error("expected error when changing TLS")
	}
}

func TestLoginWebhook_ValidateUpdate_RolesCanChange(t *testing.T) {
	oldLogin := validLogin()
	oldLogin.Spec.ServerRoles = []string{"dbcreator"}

	newLogin := validLogin()
	newLogin.Spec.ServerRoles = []string{"dbcreator", "securityadmin"}

	_, err := newLogin.ValidateUpdate(oldLogin)
	if err != nil {
		t.Errorf("expected roles to be mutable, got %v", err)
	}
}

func validLogin() *Login {
	port := int32(1433)
	return &Login{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-login",
			Namespace: "default",
		},
		Spec: LoginSpec{
			LoginName:      "mylogin",
			PasswordSecret: SecretReference{Name: "login-password"},
			Server: ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: SecretReference{Name: "sa-credentials"},
			},
		},
	}
}
