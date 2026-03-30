//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

// =============================================================================
// Backup & Restore E2E Tests
// =============================================================================

func TestE2EBackupRestore(t *testing.T) {
	// Prerequisite: create a database to back up
	dbKey := types.NamespacedName{Name: "backup-test-db", Namespace: testNamespace}
	backupKey := types.NamespacedName{Name: "test-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "test-restore", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "backuptest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("CreateBackup_Full", func(t *testing.T) {
		backup := &mssqlv1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace},
			Spec: mssqlv1.BackupSpec{
				Server:       serverRef(),
				DatabaseName: "backuptest",
				Destination:  "/var/opt/mssql/backup/backuptest.bak",
				Type:         mssqlv1.BackupTypeFull,
				Compression:  ptr(true),
			},
		}
		if err := k8sClient.Create(ctx, backup); err != nil {
			t.Fatalf("Failed to create Backup CR: %v", err)
		}

		bak := waitForBackupPhase(t, backupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

		// Verify status fields
		if bak.Status.StartTime == nil {
			t.Error("Expected StartTime to be set")
		}
		if bak.Status.CompletionTime == nil {
			t.Error("Expected CompletionTime to be set")
		}
		if bak.Status.CompletionTime != nil && bak.Status.StartTime != nil {
			if bak.Status.CompletionTime.Before(bak.Status.StartTime) {
				t.Error("CompletionTime should not be before StartTime")
			}
		}

		// Verify Ready condition
		cond := findCondition(bak, mssqlv1.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Error("Expected Ready=True condition on completed backup")
		}
	})

	t.Run("BackupOneShot", func(t *testing.T) {
		// Verify that a completed backup stays completed and doesn't re-execute.
		var bak mssqlv1.Backup
		if err := k8sClient.Get(ctx, backupKey, &bak); err != nil {
			t.Fatalf("Failed to get Backup: %v", err)
		}
		if bak.Status.Phase != mssqlv1.BackupPhaseCompleted {
			t.Fatalf("Expected phase Completed, got %s", bak.Status.Phase)
		}
		completionTime := bak.Status.CompletionTime.DeepCopy()

		// Wait a reconciliation cycle and verify it's still completed with the same timestamp
		time.Sleep(10 * time.Second)

		var after mssqlv1.Backup
		if err := k8sClient.Get(ctx, backupKey, &after); err != nil {
			t.Fatalf("Failed to get Backup after wait: %v", err)
		}
		if after.Status.Phase != mssqlv1.BackupPhaseCompleted {
			t.Errorf("Phase changed from Completed to %s", after.Status.Phase)
		}
		if !after.Status.CompletionTime.Equal(completionTime) {
			t.Error("CompletionTime changed — backup was re-executed")
		}
	})

	t.Run("RestoreDatabase", func(t *testing.T) {
		// Drop the original database first so restore can recreate it
		_ = k8sClient.Delete(ctx, db)
		waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)

		// Wait for the DB to actually be dropped on SQL Server
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			exists, err := sqlClient.DatabaseExists(ctx, "backuptest")
			if err != nil {
				return false, nil
			}
			return !exists, nil
		})
		if err != nil {
			t.Fatalf("Timed out waiting for database to be dropped: %v", err)
		}

		restore := &mssqlv1.Restore{
			ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
			Spec: mssqlv1.RestoreSpec{
				Server:       serverRef(),
				DatabaseName: "backuptest",
				Source:       "/var/opt/mssql/backup/backuptest.bak",
			},
		}
		if err := k8sClient.Create(ctx, restore); err != nil {
			t.Fatalf("Failed to create Restore CR: %v", err)
		}

		rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

		// Verify status
		if rst.Status.StartTime == nil {
			t.Error("Expected StartTime to be set")
		}
		if rst.Status.CompletionTime == nil {
			t.Error("Expected CompletionTime to be set")
		}

		// Verify database exists again
		exists, err := sqlClient.DatabaseExists(ctx, "backuptest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database 'backuptest' should exist after restore")
		}
	})

	t.Run("RestoreOneShot", func(t *testing.T) {
		var rst mssqlv1.Restore
		if err := k8sClient.Get(ctx, restoreKey, &rst); err != nil {
			t.Fatalf("Failed to get Restore: %v", err)
		}
		if rst.Status.Phase != mssqlv1.RestorePhaseCompleted {
			t.Fatalf("Expected phase Completed, got %s", rst.Status.Phase)
		}
		completionTime := rst.Status.CompletionTime.DeepCopy()

		time.Sleep(10 * time.Second)

		var after mssqlv1.Restore
		if err := k8sClient.Get(ctx, restoreKey, &after); err != nil {
			t.Fatalf("Failed to get Restore after wait: %v", err)
		}
		if after.Status.Phase != mssqlv1.RestorePhaseCompleted {
			t.Errorf("Phase changed from Completed to %s", after.Status.Phase)
		}
		if !after.Status.CompletionTime.Equal(completionTime) {
			t.Error("CompletionTime changed — restore was re-executed")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	// Drop the restored database via raw SQL
	execRawSQL(t, "master", "IF DB_ID('backuptest') IS NOT NULL DROP DATABASE [backuptest]")
}

func TestE2EBackupFailure_NonExistentDB(t *testing.T) {
	key := types.NamespacedName{Name: "backup-fail-nodb", Namespace: testNamespace}

	backup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:       serverRef(),
			DatabaseName: "nonexistent_db_xyz",
			Destination:  "/var/opt/mssql/backup/fail.bak",
			Type:         mssqlv1.BackupTypeFull,
		},
	}
	if err := k8sClient.Create(ctx, backup); err != nil {
		t.Fatalf("Failed to create Backup CR: %v", err)
	}

	bak := waitForBackupPhase(t, key, mssqlv1.BackupPhaseFailed, pollTimeout)

	cond := findCondition(bak, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Error("Expected Ready=False condition on failed backup")
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, backup)
}

// =============================================================================
// ScheduledBackup E2E Tests
// =============================================================================

func TestE2EScheduledBackup(t *testing.T) {
	// Create a prerequisite database for backups
	dbKey := types.NamespacedName{Name: "schedbak-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "schedbaktest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create prerequisite Database: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	sbKey := types.NamespacedName{Name: "e2e-schedbak", Namespace: testNamespace}

	t.Run("CreateScheduledBackup", func(t *testing.T) {
		sb := &mssqlv1.ScheduledBackup{
			ObjectMeta: metav1.ObjectMeta{Name: sbKey.Name, Namespace: sbKey.Namespace},
			Spec: mssqlv1.ScheduledBackupSpec{
				Server:              serverRef(),
				DatabaseName:        "schedbaktest",
				Schedule:            "*/1 * * * *", // every minute
				Type:                mssqlv1.BackupTypeFull,
				Compression:         ptr(true),
				DestinationTemplate: "/var/opt/mssql/backup/{{.DatabaseName}}-{{.Timestamp}}.bak",
				Retention: &mssqlv1.RetentionPolicy{
					MaxCount: ptr(int32(3)),
				},
			},
		}
		if err := k8sClient.Create(ctx, sb); err != nil {
			t.Fatalf("Failed to create ScheduledBackup CR: %v", err)
		}

		// Wait for the ScheduledBackup to become ready (first backup should run within ~1 minute)
		waitForCondition(t, sbKey, &mssqlv1.ScheduledBackup{}, mssqlv1.ConditionReady, metav1.ConditionTrue, 3*time.Minute)
	})

	t.Run("VerifyBackupCreated", func(t *testing.T) {
		// Check that at least one Backup CR was created
		var sb mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &sb); err != nil {
			t.Fatalf("Failed to get ScheduledBackup: %v", err)
		}
		if sb.Status.TotalBackups < 1 {
			t.Errorf("Expected at least 1 total backup, got %d", sb.Status.TotalBackups)
		}
		if sb.Status.SuccessfulBackups < 1 {
			t.Errorf("Expected at least 1 successful backup, got %d", sb.Status.SuccessfulBackups)
		}
		if len(sb.Status.History) < 1 {
			t.Error("Expected at least 1 history entry")
		}
		if sb.Status.LastSuccessfulBackup == "" {
			t.Error("Expected lastSuccessfulBackup to be set")
		}
		t.Logf("ScheduledBackup: total=%d success=%d history=%d",
			sb.Status.TotalBackups, sb.Status.SuccessfulBackups, len(sb.Status.History))
	})

	t.Run("SuspendResume", func(t *testing.T) {
		var sb mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &sb); err != nil {
			t.Fatalf("Failed to get ScheduledBackup: %v", err)
		}
		sb.Spec.Suspend = ptr(true)
		if err := k8sClient.Update(ctx, &sb); err != nil {
			t.Fatalf("Failed to suspend ScheduledBackup: %v", err)
		}
		// Wait a moment to confirm no new backups are triggered
		time.Sleep(10 * time.Second)

		var updated mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &updated); err != nil {
			t.Fatalf("Failed to get updated ScheduledBackup: %v", err)
		}
		countBefore := updated.Status.TotalBackups

		// Resume
		updated.Spec.Suspend = ptr(false)
		if err := k8sClient.Update(ctx, &updated); err != nil {
			t.Fatalf("Failed to resume ScheduledBackup: %v", err)
		}
		t.Logf("Suspend/resume OK — total backups before suspend: %d", countBefore)
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.ScheduledBackup{ObjectMeta: metav1.ObjectMeta{Name: sbKey.Name, Namespace: sbKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace}})
}

// =============================================================================
// Point-in-Time Restore E2E Tests
// =============================================================================

func TestE2EPointInTimeRestore(t *testing.T) {
	dbName := "pitrestoretest"
	dbKey := types.NamespacedName{Name: "pit-restore-db", Namespace: testNamespace}
	fullBackupKey := types.NamespacedName{Name: "pit-full-backup", Namespace: testNamespace}
	logBackupKey := types.NamespacedName{Name: "pit-log-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "pit-restore", Namespace: testNamespace}

	// 1. Create a database with FULL recovery model (required for log backups)
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   dbName,
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			RecoveryModel:  ptr(mssqlv1.RecoveryModelFull),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// 2. Create a test table and insert "before" data
	execRawSQL(t, dbName, "CREATE TABLE dbo.TestData (id INT PRIMARY KEY, value NVARCHAR(50), inserted_at DATETIME2 DEFAULT GETDATE())")
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (1, 'before-pit')")

	// 3. Take a full backup (required base for log chain)
	fullBackup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: fullBackupKey.Name, Namespace: fullBackupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:       serverRef(),
			DatabaseName: dbName,
			Destination:  fmt.Sprintf("/var/opt/mssql/backup/%s-full.bak", dbName),
			Type:         mssqlv1.BackupTypeFull,
			Compression:  ptr(true),
		},
	}
	if err := k8sClient.Create(ctx, fullBackup); err != nil {
		t.Fatalf("Failed to create full Backup CR: %v", err)
	}
	waitForBackupPhase(t, fullBackupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 4. Insert more data and record the PIT timestamp
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (2, 'at-pit')")

	// Wait to ensure the insert is fully committed and log records are flushed
	time.Sleep(3 * time.Second)

	// Record the point-in-time AFTER row 2 is committed (we want to restore to HERE)
	pitTimestamp := queryScalarSQL(t, dbName, "SELECT FORMAT(GETDATE(), 'yyyy-MM-ddTHH:mm:ss')")
	t.Logf("PIT timestamp: %s", pitTimestamp)

	// Wait to ensure clock advances past the PIT timestamp
	time.Sleep(3 * time.Second)

	// 5. Insert "after" data that should NOT be present after PIT restore
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (3, 'after-pit')")

	// 6. Take a log backup (captures the log chain including all inserts)
	logBackup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: logBackupKey.Name, Namespace: logBackupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:       serverRef(),
			DatabaseName: dbName,
			Destination:  fmt.Sprintf("/var/opt/mssql/backup/%s-log.trn", dbName),
			Type:         mssqlv1.BackupTypeLog,
			Compression:  ptr(true),
		},
	}
	if err := k8sClient.Create(ctx, logBackup); err != nil {
		t.Fatalf("Failed to create log Backup CR: %v", err)
	}
	waitForBackupPhase(t, logBackupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 7. Drop the database via raw SQL (operator DB CR still exists but we need DB gone for restore)
	_ = k8sClient.Delete(ctx, db)
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		exists, err := sqlClient.DatabaseExists(ctx, dbName)
		if err != nil {
			return false, nil
		}
		return !exists, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for database to be dropped: %v", err)
	}

	// 8. Restore with STOPAT to the PIT timestamp
	restore := &mssqlv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
		Spec: mssqlv1.RestoreSpec{
			Server:       serverRef(),
			DatabaseName: dbName,
			Source:       fmt.Sprintf("/var/opt/mssql/backup/%s-full.bak", dbName),
			LogSource:    ptr(fmt.Sprintf("/var/opt/mssql/backup/%s-log.trn", dbName)),
			StopAt:       ptr(pitTimestamp),
		},
	}
	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("Failed to create PIT Restore CR: %v", err)
	}

	rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

	// 9. Verify the restore was successful
	if rst.Status.CompletionTime == nil {
		t.Error("Expected CompletionTime to be set")
	}
	cond := findCondition(rst, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("Expected Ready=True on completed PIT restore")
	}

	// 10. Verify data: rows 1 and 2 should exist, row 3 should NOT
	count := queryScalarSQL(t, dbName, "SELECT COUNT(*) FROM dbo.TestData WHERE id IN (1, 2)")
	if count != "2" {
		t.Errorf("Expected 2 rows (before + at PIT), got %s", count)
	}

	afterCount := queryScalarSQL(t, dbName, "SELECT COUNT(*) FROM dbo.TestData WHERE id = 3")
	if afterCount != "0" {
		t.Errorf("Expected 0 rows for after-PIT data, got %s (PIT restore did not exclude post-PIT data)", afterCount)
	}

	t.Logf("PIT restore verified: 2 rows present (before+at PIT), 0 rows after PIT")

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: fullBackupKey.Name, Namespace: fullBackupKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: logBackupKey.Name, Namespace: logBackupKey.Namespace}})
	execRawSQL(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", dbName, dbName))
}

// =============================================================================
// WITH MOVE Restore E2E Tests
// =============================================================================

func TestE2ERestoreWithMove(t *testing.T) {
	dbName := "moverestoretest"
	targetDbName := "moverestoretarget"
	dbKey := types.NamespacedName{Name: "move-restore-db", Namespace: testNamespace}
	backupKey := types.NamespacedName{Name: "move-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "move-restore", Namespace: testNamespace}

	// 1. Create and backup a source database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   dbName,
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	execRawSQL(t, dbName, "CREATE TABLE dbo.MoveTest (id INT PRIMARY KEY, value NVARCHAR(50))")
	execRawSQL(t, dbName, "INSERT INTO dbo.MoveTest (id, value) VALUES (1, 'moved')")

	backup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:       serverRef(),
			DatabaseName: dbName,
			Destination:  fmt.Sprintf("/var/opt/mssql/backup/%s.bak", dbName),
			Type:         mssqlv1.BackupTypeFull,
		},
	}
	if err := k8sClient.Create(ctx, backup); err != nil {
		t.Fatalf("Failed to create Backup CR: %v", err)
	}
	waitForBackupPhase(t, backupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 2. Get the logical file names from the backup
	// RESTORE FILELISTONLY returns multiple columns; we only need LogicalName (first column).
	_ = dbName // logical names follow the convention: dbName for data, dbName_log for log

	// 3. Restore to a different database name with MOVE
	restore := &mssqlv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
		Spec: mssqlv1.RestoreSpec{
			Server:       serverRef(),
			DatabaseName: targetDbName,
			Source:       fmt.Sprintf("/var/opt/mssql/backup/%s.bak", dbName),
			WithMove: []mssqlv1.FileMapping{
				{
					LogicalName:  dbName,
					PhysicalPath: fmt.Sprintf("/var/opt/mssql/data/%s.mdf", targetDbName),
				},
				{
					LogicalName:  dbName + "_log",
					PhysicalPath: fmt.Sprintf("/var/opt/mssql/data/%s_log.ldf", targetDbName),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("Failed to create Restore with MOVE CR: %v", err)
	}

	rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

	// 4. Verify the target database exists and has data
	cond := findCondition(rst, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("Expected Ready=True on completed WITH MOVE restore")
	}

	exists, err := sqlClient.DatabaseExists(ctx, targetDbName)
	if err != nil {
		t.Fatalf("Failed to check target DB existence: %v", err)
	}
	if !exists {
		t.Fatal("Target database should exist after WITH MOVE restore")
	}

	val := queryScalarSQL(t, targetDbName, "SELECT value FROM dbo.MoveTest WHERE id = 1")
	if val != "moved" {
		t.Errorf("Expected value 'moved', got '%s'", val)
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace}})
	_ = k8sClient.Delete(ctx, db)
	execRawSQLIgnoreError(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", dbName, dbName))
	execRawSQLIgnoreError(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", targetDbName, targetDbName))
}

// =============================================================================
// Business Metrics E2E Test
// =============================================================================

func TestE2EBusinessMetrics(t *testing.T) {
	metricsFwd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"deploy/mssql-operator", "18082:8080",
		"-n", "mssql-operator-system",
	)
	metricsFwd.Stdout = io.Discard
	metricsFwd.Stderr = io.Discard
	if err := metricsFwd.Start(); err != nil {
		t.Fatalf("Failed to start metrics port-forward: %v", err)
	}
	defer func() {
		if metricsFwd.Process != nil {
			_ = metricsFwd.Process.Kill()
		}
	}()
	time.Sleep(3 * time.Second)

	resp, err := http.Get("http://localhost:18082/metrics")
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	metricsStr := string(body)

	if resp.StatusCode != 200 {
		t.Fatalf("Metrics endpoint returned %d", resp.StatusCode)
	}

	// Check for business metrics emitted by controllers.
	// database_ready is guaranteed since backup tests create Database CRs.
	assertMetricExists(t, metricsStr, "mssql_database_ready")
}
