package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RetentionPolicy defines how old backups are cleaned up.
type RetentionPolicy struct {
	// MaxCount is the maximum number of successful backups to retain.
	// Older backups beyond this count are deleted. 0 means unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxCount *int32 `json:"maxCount,omitempty"`

	// MaxAge is the maximum age of a backup to retain (e.g. "168h" for 7 days).
	// Backups older than this are deleted.
	// +optional
	MaxAge *string `json:"maxAge,omitempty"`
}

// ScheduledBackupSpec defines the desired state of a ScheduledBackup.
type ScheduledBackupSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the database to back up.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// Schedule is a cron expression (e.g. "0 2 * * *" for 2 AM daily).
	// Uses standard 5-field cron format.
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Type is the backup type: Full, Differential, or Log.
	// +kubebuilder:default=Full
	Type BackupType `json:"type,omitempty"`

	// Compression enables backup compression.
	// +optional
	// +kubebuilder:default=false
	Compression *bool `json:"compression,omitempty"`

	// DestinationTemplate is a Go template for the backup file path.
	// Available variables: {{.DatabaseName}}, {{.Timestamp}}, {{.Type}}.
	// Example: /backups/{{.DatabaseName}}-{{.Timestamp}}.bak
	// +kubebuilder:validation:MinLength=1
	DestinationTemplate string `json:"destinationTemplate"`

	// Retention defines the retention policy for old backups.
	// +optional
	Retention *RetentionPolicy `json:"retention,omitempty"`

	// Suspend stops the schedule from creating new backups when true.
	// +optional
	// +kubebuilder:default=false
	Suspend *bool `json:"suspend,omitempty"`
}

// ScheduledBackupHistory records a single backup execution.
type ScheduledBackupHistory struct {
	// BackupName is the name of the generated Backup CR.
	BackupName string `json:"backupName"`

	// StartTime is when this backup started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when this backup completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Phase is the final phase of this backup.
	Phase BackupPhase `json:"phase,omitempty"`

	// Destination is the file path of the backup.
	Destination string `json:"destination,omitempty"`
}

// ScheduledBackupStatus defines the observed state of a ScheduledBackup.
type ScheduledBackupStatus struct {
	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastScheduleTime is the last time a Backup was created from this schedule.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// NextScheduleTime is the next time a Backup will be created.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// LastSuccessfulBackup is the name of the last successfully completed Backup.
	// +optional
	LastSuccessfulBackup string `json:"lastSuccessfulBackup,omitempty"`

	// ActiveBackup is the name of the currently running Backup CR, if any.
	// +optional
	ActiveBackup string `json:"activeBackup,omitempty"`

	// History contains the last N backup executions.
	// +optional
	History []ScheduledBackupHistory `json:"history,omitempty"`

	// TotalBackups is the total number of backups created by this schedule.
	TotalBackups int32 `json:"totalBackups,omitempty"`

	// SuccessfulBackups is the number of successful backups.
	SuccessfulBackups int32 `json:"successfulBackups,omitempty"`

	// FailedBackups is the number of failed backups.
	FailedBackups int32 `json:"failedBackups,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msschedbak,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Last",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduledBackup is the Schema for the scheduledbackups API.
type ScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduledBackupSpec   `json:"spec,omitempty"`
	Status ScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScheduledBackupList contains a list of ScheduledBackup.
type ScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScheduledBackup{}, &ScheduledBackupList{})
}
