package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

type fakeStartupTaskStore struct {
	value                        startupTaskValue
	readErr, writeErr, deleteErr error
	writes, deletes              int
}

func (s *fakeStartupTaskStore) Read() (startupTaskValue, error) { return s.value, s.readErr }
func (s *fakeStartupTaskStore) Write(text string) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.value = startupTaskValue{exists: true, xml: text}
	return nil
}
func (s *fakeStartupTaskStore) Delete() error {
	s.deletes++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.value = startupTaskValue{}
	return nil
}

func TestStartupTaskDefinition(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "翻译 & tools")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "gooeslate.exe")
	if err := os.WriteFile(exe, []byte("not an executable"), 0600); err != nil {
		t.Fatal(err)
	}
	sid := "S-1-5-21-123-456-789-1001"
	text, err := startupTaskXML(exe, sid)
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		XMLName   xml.Name                                                                                                                                                                                                            `xml:"Task"`
		Trigger   struct{ UserId, Delay, Enabled string }                                                                                                                                                                             `xml:"Triggers>LogonTrigger"`
		Principal struct{ UserId, LogonType, RunLevel string }                                                                                                                                                                        `xml:"Principals>Principal"`
		Settings  struct{ MultipleInstancesPolicy, DisallowStartIfOnBatteries, StopIfGoingOnBatteries, StartWhenAvailable, RunOnlyIfNetworkAvailable, AllowStartOnDemand, Enabled, Hidden, RunOnlyIfIdle, ExecutionTimeLimit string } `xml:"Settings"`
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := decoder.Decode(&values); err != nil {
		t.Fatal(err)
	}
	// UserId is case-sensitive in Task Scheduler's schema.
	if !strings.Contains(text, "<UserId>"+sid+"</UserId>") || values.Principal.UserId != sid || values.Principal.LogonType != "InteractiveToken" || values.Principal.RunLevel != "LeastPrivilege" {
		t.Fatalf("wrong principal: %+v", values.Principal)
	}
	if values.Trigger.UserId != sid || values.Trigger.Delay != "PT10S" || values.Trigger.Enabled != "true" {
		t.Fatalf("wrong trigger: %+v", values.Trigger)
	}
	s := values.Settings
	if s.MultipleInstancesPolicy != "IgnoreNew" || s.DisallowStartIfOnBatteries != "false" || s.StopIfGoingOnBatteries != "false" || s.StartWhenAvailable != "true" || s.RunOnlyIfNetworkAvailable != "false" || s.RunOnlyIfIdle != "false" || s.ExecutionTimeLimit != "PT0S" || s.Enabled != "true" || s.AllowStartOnDemand != "true" || s.Hidden != "false" {
		t.Fatalf("wrong resident-app settings: %+v", s)
	}
	definition, err := decodeStartupTask(startupTaskValue{true, text})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Actions.Exec.Command != exe || definition.Actions.Exec.Arguments != "--autostart" || definition.Actions.Exec.WorkingDirectory != dir {
		t.Fatalf("wrong action: %+v", definition.Actions.Exec)
	}
	if values.XMLName.Space != "http://schemas.microsoft.com/windows/2004/02/mit/task" {
		t.Fatal("missing task namespace")
	}
}

func TestStartupTaskStatusPrefersTaskAndFallsBackOnlyWhenAbsent(t *testing.T) {
	for _, fixture := range []struct {
		name, xml    string
		exists, want bool
		wantError    bool
	}{
		{"legacy", "", false, true, false},
		{"task enabled", "<Task><Settings><Enabled>true</Enabled></Settings></Task>", true, true, false},
		{"task explicitly disabled", "<Task><Settings><Enabled>false</Enabled></Settings></Task>", true, false, false},
		{"task default enabled", "<Task/>", true, true, false},
		{"task malformed", "<Task>", true, false, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			tasks := &fakeStartupTaskStore{value: startupTaskValue{fixture.exists, fixture.xml}}
			legacy := &fakeStartupStore{value: startupStringValue(`"C:\old.exe" --autostart`)}
			if fixture.exists {
				legacy.readErr = errors.New("must not read Run when task exists")
			}
			got, err := startupStatusWithStores(tasks, legacy)
			if got != fixture.want || (err != nil) != fixture.wantError {
				t.Fatalf("enabled=%v err=%v", got, err)
			}
		})
	}
	if _, err := startupStatusWithStores(&fakeStartupTaskStore{readErr: errors.New("denied")}, &fakeStartupStore{}); err == nil {
		t.Fatal("task access failure hidden")
	}
}

func TestStartupTaskMigrationAndRollback(t *testing.T) {
	exe := startupTestExecutable(t)
	oldRun := startupStringValue(`"C:\old.exe" --autostart`)
	legacy := &fakeStartupStore{value: oldRun}
	tasks := &fakeStartupTaskStore{}
	change, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !change.CurrentEnabled() || !change.NeedsApply(true) {
		t.Fatal("enabled legacy Run must need migration")
	}
	if tasks.writes != 0 || legacy.deletes != 0 {
		t.Fatal("prepare mutated startup")
	}
	if err := change.Apply(true); err != nil {
		t.Fatal(err)
	}
	if !tasks.value.exists || legacy.value.exists {
		t.Fatal("migration did not replace Run with task")
	}
	current, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !current.CurrentEnabled() || current.NeedsApply(true) {
		t.Fatal("unchanged enabled task should not need rewriting")
	}
	if !current.NeedsApply(false) {
		t.Fatal("turning off must remove task")
	}
	if err := current.Apply(false); err != nil {
		t.Fatal(err)
	}
	if tasks.value.exists || legacy.value.exists {
		t.Fatal("disable left a startup registration")
	}
	if err := current.Rollback(); err != nil {
		t.Fatal(err)
	}
	if tasks.value.xml != current.beforeTask.xml {
		t.Fatal("rollback did not preserve entire original task XML")
	}
	if err := change.Rollback(); err != nil {
		t.Fatal(err)
	}
	if tasks.value.exists || !bytes.Equal(legacy.value.data, oldRun.data) || legacy.value.typeID != oldRun.typeID {
		t.Fatal("migration rollback lost old Run")
	}
	writes, deletes := tasks.writes, tasks.deletes
	if err := change.Rollback(); err != nil || writes != tasks.writes || deletes != tasks.deletes {
		t.Fatal("rollback not idempotent")
	}
}

func TestStartupTaskPathChangeAndDisabledTaskNeedApply(t *testing.T) {
	exe := startupTestExecutable(t)
	text, err := startupTaskXML(exe, "S-1-5-21-1001")
	if err != nil {
		t.Fatal(err)
	}
	for _, prior := range []string{
		strings.ReplaceAll(text, "<Enabled>true</Enabled>", "<Enabled>false</Enabled>"),
		strings.Replace(text, "<Arguments>--autostart</Arguments>", "<Arguments>--other</Arguments>", 1),
		strings.Replace(text, "<Command>", "<Command>C:\\old\\", 1),
	} {
		change, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", &fakeStartupTaskStore{value: startupTaskValue{true, prior}}, &fakeStartupStore{})
		if err != nil {
			t.Fatal(err)
		}
		if !change.NeedsApply(true) {
			t.Fatal("changed task not detected")
		}
	}
}

func TestStartupTaskPartialFailureRestoresBothStores(t *testing.T) {
	exe := startupTestExecutable(t)
	text, err := startupTaskXML(exe, "S-1-5-21-1001")
	if err != nil {
		t.Fatal(err)
	}
	oldTask := startupTaskValue{true, strings.Replace(text, "Gooeslate login startup", "custom description", 1)}
	oldRun := startupStringValue(`"C:\old.exe" --autostart`)
	for _, enable := range []bool{false, true} {
		t.Run(fmt.Sprint(enable), func(t *testing.T) {
			tasks := &fakeStartupTaskStore{value: oldTask}
			legacy := &fakeStartupStore{value: oldRun, deleteErr: errors.New("Run removal denied")}
			change, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := change.Apply(enable); err == nil || !strings.Contains(err.Error(), "Run removal denied") {
				t.Fatalf("expected failure, got %v", err)
			}
			if tasks.value != oldTask || !bytes.Equal(legacy.value.data, oldRun.data) || change.applied {
				t.Fatal("failed Apply did not roll back both stores")
			}
		})
	}
	// A failed restore remains retryable and still attempts the other store.
	tasks := &fakeStartupTaskStore{value: oldTask}
	legacy := &fakeStartupStore{value: oldRun}
	change, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Apply(true); err != nil {
		t.Fatal(err)
	}
	tasks.writeErr = errors.New("restore denied")
	if err := change.Rollback(); err == nil || !change.applied {
		t.Fatal("failed restore discarded rollback state")
	}
	if !bytes.Equal(legacy.value.data, oldRun.data) {
		t.Fatal("task restore failure prevented restoring Run")
	}
	tasks.writeErr = nil
	if err := change.Rollback(); err != nil || change.applied || tasks.value != oldTask {
		t.Fatalf("rollback retry failed: %v", err)
	}
}

func TestStartupTaskReadAndWriteFailures(t *testing.T) {
	exe := startupTestExecutable(t)
	denied := errors.New("denied")
	tasks := &fakeStartupTaskStore{readErr: denied}
	legacy := &fakeStartupStore{}
	if _, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy); !errors.Is(err, denied) {
		t.Fatal("read failure hidden")
	}
	tasks.readErr = nil
	legacy.readErr = denied
	if _, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy); !errors.Is(err, denied) {
		t.Fatal("legacy read failure hidden")
	}
	legacy.readErr = nil
	tasks.writeErr = denied
	change, err := prepareScheduledStartupChange(exe, "S-1-5-21-1001", tasks, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Apply(true); !errors.Is(err, denied) || legacy.deletes != 0 {
		t.Fatal("task write failure removed legacy registration")
	}
	tasks.deleteErr = denied
	if err := change.Apply(false); !errors.Is(err, denied) || legacy.deletes != 0 {
		t.Fatal("task delete failure removed legacy registration")
	}
}

// Opt-in OS test. It owns one unique disposable task, never the app's task or Run
// value. The action points at an inert temporary fixture and is never executed.
func TestStartupTaskNativeStore(t *testing.T) {
	if os.Getenv("GOOESLATE_TEST_STARTUP_TASK") != "1" {
		t.Skip("set GOOESLATE_TEST_STARTUP_TASK=1 for isolated Task Scheduler integration")
	}
	sid, _, err := startupTaskIdentity()
	if err != nil {
		t.Fatal(err)
	}
	store := windowsStartupTaskStore{name: fmt.Sprintf("Gooeslate-Test-%s-%d-%d", sid, os.Getpid(), time.Now().UnixNano()), sid: sid}
	value, err := store.Read()
	if err != nil || value.exists {
		t.Fatalf("expected absent test task: exists=%v err=%v", value.exists, err)
	}
	t.Cleanup(func() {
		if err := store.Delete(); err != nil {
			t.Errorf("remove isolated test task: %v", err)
		}
	})
	text, err := startupTaskXML(startupTestExecutable(t), sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(text); err != nil {
		t.Fatal(err)
	}
	value, err = store.Read()
	if err != nil || !value.exists {
		t.Fatalf("registered task not readable: %v", err)
	}
	if enabled, err := startupTaskEnabled(value); err != nil || !enabled {
		t.Fatalf("registered task not enabled: %v", err)
	}
	disabled := strings.Replace(text, "<Settings>", "<Settings><Enabled>false</Enabled>", 1)
	disabled = strings.Replace(disabled, "<Enabled>true</Enabled><Hidden>", "<Hidden>", 1)
	if err := store.Write(disabled); err != nil {
		t.Fatal(err)
	}
	value, err = store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := startupTaskEnabled(value); err != nil || enabled {
		t.Fatalf("disabled task reported enabled: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Read(); err != nil || value.exists {
		t.Fatalf("deleted task still exists: %v", err)
	}
}

func TestStartupTaskRollbackPreservesLegacyTypeAndBytes(t *testing.T) {
	for _, oldRun := range []startupValue{
		{exists: true, typeID: registry.EXPAND_SZ, data: startupStringValue(`"%LOCALAPPDATA%\old.exe" --autostart`).data},
		{exists: true, typeID: registry.BINARY, data: []byte{1, 0, 255, 0, 17}},
	} {
		tasks := &fakeStartupTaskStore{}
		legacy := &fakeStartupStore{value: oldRun}
		change, err := prepareScheduledStartupChange(startupTestExecutable(t), "S-1-5-21-1001", tasks, legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := change.Apply(false); err != nil {
			t.Fatal(err)
		}
		if err := change.Rollback(); err != nil {
			t.Fatal(err)
		}
		if !legacy.value.exists || legacy.value.typeID != oldRun.typeID || !bytes.Equal(legacy.value.data, oldRun.data) {
			t.Fatal("rollback did not preserve original Run type and bytes")
		}
	}
}
