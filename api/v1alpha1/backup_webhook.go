package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Backup) Default() {
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
	if r.Spec.Type == "" {
		r.Spec.Type = BackupTypeFull
	}
	if r.Spec.Compression == nil {
		c := false
		r.Spec.Compression = &c
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Backup) ValidateCreate() ([]string, error) {
	return nil, r.validateBackup()
}

// ValidateUpdate implements webhook.Validator.
func (r *Backup) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldBackup, ok := old.(*Backup)
	if !ok {
		return nil, fmt.Errorf("expected Backup, got %T", old)
	}

	var allErrs field.ErrorList

	// Spec is fully immutable — backup is a one-shot operation
	if r.Spec.DatabaseName != oldBackup.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"spec is immutable after creation"))
	}
	if r.Spec.Destination != oldBackup.Spec.Destination {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "destination"),
			"spec is immutable after creation"))
	}
	if r.Spec.Type != oldBackup.Spec.Type {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "type"),
			"spec is immutable after creation"))
	}
	if r.Spec.Server.Host != oldBackup.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"spec is immutable after creation"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator.
func (r *Backup) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Backup) validateBackup() error {
	var allErrs field.ErrorList

	if r.Spec.DatabaseName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "databaseName"),
			"databaseName is required"))
	}
	if r.Spec.Destination == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "destination"),
			"destination is required"))
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

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
