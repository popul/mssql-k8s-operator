package v1alpha1

import (
	"fmt"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements defaulting for ScheduledBackup.
func (r *ScheduledBackup) Default() {
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
	if r.Spec.Suspend == nil {
		s := false
		r.Spec.Suspend = &s
	}
}

// ValidateCreate validates a new ScheduledBackup.
func (r *ScheduledBackup) ValidateCreate() ([]string, error) {
	return nil, r.validate()
}

// ValidateUpdate validates an update to ScheduledBackup.
func (r *ScheduledBackup) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldSB, ok := old.(*ScheduledBackup)
	if !ok {
		return nil, fmt.Errorf("expected ScheduledBackup, got %T", old)
	}

	var allErrs field.ErrorList

	// databaseName is immutable
	if r.Spec.DatabaseName != oldSB.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"databaseName is immutable",
		))
	}

	if err := r.validate(); err != nil {
		return nil, err
	}
	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete validates deletion.
func (r *ScheduledBackup) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *ScheduledBackup) validate() error {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if r.Spec.DatabaseName == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("databaseName"), "must be specified"))
	}
	if r.Spec.Schedule == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("schedule"), "must be specified"))
	} else {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(r.Spec.Schedule); err != nil {
			allErrs = append(allErrs, field.Invalid(specPath.Child("schedule"), r.Spec.Schedule,
				fmt.Sprintf("invalid cron expression: %v", err)))
		}
	}
	if r.Spec.DestinationTemplate == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("destinationTemplate"), "must be specified"))
	}
	if r.Spec.Server.Host == "" && r.Spec.Server.SQLServerRef == nil {
		allErrs = append(allErrs, field.Required(specPath.Child("server"),
			"must specify either server.host or server.sqlServerRef"))
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
