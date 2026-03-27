package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Login) Default() {
	if r.Spec.DeletionPolicy == nil {
		policy := DeletionPolicyRetain
		r.Spec.DeletionPolicy = &policy
	}
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Login) ValidateCreate() ([]string, error) {
	return nil, r.validateLogin()
}

// ValidateUpdate implements webhook.Validator.
func (r *Login) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldLogin, ok := old.(*Login)
	if !ok {
		return nil, fmt.Errorf("expected Login, got %T", old)
	}

	var allErrs field.ErrorList

	// loginName is immutable
	if r.Spec.LoginName != oldLogin.Spec.LoginName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "loginName"),
			"loginName is immutable after creation"))
	}

	// server reference is immutable
	if r.Spec.Server.Host != oldLogin.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable"))
	}

	if !int32PtrEqual(r.Spec.Server.Port, oldLogin.Spec.Server.Port) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "port"),
			"server port is immutable"))
	}

	if !boolPtrEqual(r.Spec.Server.TLS, oldLogin.Spec.Server.TLS) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "tls"),
			"server tls is immutable"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, r.validateLogin()
}

// ValidateDelete implements webhook.Validator.
func (r *Login) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Login) validateLogin() error {
	var allErrs field.ErrorList

	if r.Spec.LoginName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "loginName"),
			"loginName is required"))
	}

	if r.Spec.Server.Host == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "host"),
			"server host is required"))
	}

	if r.Spec.Server.CredentialsSecret.Name == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "credentialsSecret", "name"),
			"credentialsSecret name is required"))
	}

	if r.Spec.PasswordSecret.Name == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "passwordSecret", "name"),
			"passwordSecret name is required"))
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
