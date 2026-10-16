package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

type startupTaskValue struct {
	exists bool
	xml    string
}

type startupTaskStore interface {
	Read() (startupTaskValue, error)
	Write(string) error
	Delete() error
}

// Each account owns a separate task, using its existing interactive session.
func startupTaskIdentity() (sid, name string, err error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid = user.User.Sid.String()
	return sid, "Gooeslate-" + sid, nil
}

func startupTaskXML(executable, sid string) (string, error) {
	if _, err := startupCommand(executable); err != nil {
		return "", err
	}
	if sid == "" {
		return "", fmt.Errorf("无法确定开机自启用户")
	}
	escape := func(value string) string {
		var out strings.Builder
		xml.EscapeText(&out, []byte(value))
		return out.String()
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Gooeslate login startup</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><Delay>PT10S</Delay><UserId>%s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="CurrentUser"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>false</Hidden><RunOnlyIfIdle>false</RunOnlyIfIdle><ExecutionTimeLimit>PT0S</ExecutionTimeLimit></Settings>
  <Actions Context="CurrentUser"><Exec><Command>%s</Command><Arguments>--autostart</Arguments><WorkingDirectory>%s</WorkingDirectory></Exec></Actions>
</Task>`, escape(sid), escape(sid), escape(filepath.Clean(executable)), escape(filepath.Dir(executable))), nil
}

type startupTaskDefinition struct {
	XMLName  xml.Name `xml:"Task"`
	Settings struct {
		Enabled *bool `xml:"Enabled"`
	} `xml:"Settings"`
	Actions struct {
		Exec struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func decodeStartupTask(value startupTaskValue) (startupTaskDefinition, error) {
	var definition startupTaskDefinition
	// The XML comes from a BSTR, so it is already decoded despite its XML declaration.
	decoder := xml.NewDecoder(strings.NewReader(value.xml))
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	err := decoder.Decode(&definition)
	return definition, err
}

func startupTaskEnabled(value startupTaskValue) (bool, error) {
	if !value.exists {
		return false, nil
	}
	definition, err := decodeStartupTask(value)
	if err != nil {
		return false, fmt.Errorf("开机自启任务格式异常：%w", err)
	}
	return definition.Settings.Enabled == nil || *definition.Settings.Enabled, nil
}

type windowsStartupTaskStore struct{ name, sid string }

func startupCOMMissing(err error) bool {
	var oleErr *ole.OleError
	if !errors.As(err, &oleErr) {
		return false
	}
	if oleErr.Code() == 0x80070002 {
		return true
	}
	if detail, ok := oleErr.SubError().(ole.EXCEPINFO); ok {
		return detail.SCODE() == 0x80070002
	}
	return false
}

func withStartupTaskFolder(action func(*ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	var oleErr *ole.OleError
	if err != nil && !(errors.As(err, &oleErr) && oleErr.Code() == 1) {
		return err
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return err
	}
	defer unknown.Release()
	service, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer service.Release()
	connected, err := oleutil.CallMethod(service, "Connect")
	if connected != nil {
		connected.Clear()
	}
	if err != nil {
		return err
	}
	folder, err := oleutil.CallMethod(service, "GetFolder", `\`)
	if err != nil {
		return err
	}
	defer folder.Clear()
	return action(folder.ToIDispatch())
}

func (s windowsStartupTaskStore) Read() (startupTaskValue, error) {
	var value startupTaskValue
	err := withStartupTaskFolder(func(folder *ole.IDispatch) error {
		task, err := oleutil.CallMethod(folder, "GetTask", s.name)
		if startupCOMMissing(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer task.Clear()
		text, err := oleutil.GetProperty(task.ToIDispatch(), "Xml")
		if err != nil {
			return err
		}
		defer text.Clear()
		value = startupTaskValue{exists: true, xml: text.ToString()}
		return nil
	})
	return value, err
}

func (s windowsStartupTaskStore) Write(text string) error {
	return withStartupTaskFolder(func(folder *ole.IDispatch) error {
		// TASK_CREATE_OR_UPDATE=6, TASK_LOGON_INTERACTIVE_TOKEN=3. No password.
		result, err := oleutil.CallMethod(folder, "RegisterTask", s.name, text, 6, s.sid, nil, 3, nil)
		if result != nil {
			result.Clear()
		}
		return err
	})
}

func (s windowsStartupTaskStore) Delete() error {
	return withStartupTaskFolder(func(folder *ole.IDispatch) error {
		result, err := oleutil.CallMethod(folder, "DeleteTask", s.name, 0)
		if result != nil {
			result.Clear()
		}
		if startupCOMMissing(err) {
			return nil
		}
		return err
	})
}

func startupStatusWithStores(tasks startupTaskStore, legacy startupStore) (bool, error) {
	task, err := tasks.Read()
	if err != nil {
		return false, err
	}
	if task.exists {
		return startupTaskEnabled(task)
	}
	value, err := legacy.Read()
	if err != nil {
		return false, err
	}
	return startupValueEnabled(value)
}

type scheduledStartupChange struct {
	tasks      startupTaskStore
	legacy     startupStore
	beforeTask startupTaskValue
	beforeRun  startupValue
	desiredXML string
	executable string
	applied    bool
}

func prepareScheduledStartupChange(executable, sid string, tasks startupTaskStore, legacy startupStore) (*scheduledStartupChange, error) {
	text, err := startupTaskXML(executable, sid)
	if err != nil {
		return nil, err
	}
	task, err := tasks.Read()
	if err != nil {
		return nil, err
	}
	if _, err = startupTaskEnabled(task); err != nil {
		return nil, err
	}
	old, err := legacy.Read()
	if err != nil {
		return nil, err
	}
	old.data = append([]byte(nil), old.data...)
	return &scheduledStartupChange{tasks: tasks, legacy: legacy, beforeTask: task, beforeRun: old, desiredXML: text, executable: filepath.Clean(executable)}, nil
}

func (c *scheduledStartupChange) CurrentEnabled() bool {
	if c.beforeTask.exists {
		enabled, _ := startupTaskEnabled(c.beforeTask)
		return enabled
	}
	enabled, _ := startupValueEnabled(c.beforeRun)
	return enabled
}

func (c *scheduledStartupChange) NeedsApply(enabled bool) bool {
	if !enabled {
		return c.beforeTask.exists || c.beforeRun.exists
	}
	if !c.beforeTask.exists || !c.CurrentEnabled() || c.beforeRun.exists {
		return true
	}
	definition, err := decodeStartupTask(c.beforeTask)
	exec := definition.Actions.Exec
	return err != nil || !strings.EqualFold(exec.Command, c.executable) || exec.Arguments != "--autostart" || !strings.EqualFold(exec.WorkingDirectory, filepath.Dir(c.executable))
}

func (c *scheduledStartupChange) Apply(enabled bool) error {
	var err error
	if enabled {
		err = c.tasks.Write(c.desiredXML)
	} else {
		err = c.tasks.Delete()
	}
	if err != nil {
		return err
	}
	c.applied = true
	if err = c.legacy.Delete(); err != nil {
		// A failed Apply is not rolled back by the configuration transaction.
		return errors.Join(err, c.Rollback())
	}
	return nil
}

func (c *scheduledStartupChange) Rollback() error {
	if !c.applied {
		return nil
	}
	var taskErr, runErr error
	if c.beforeTask.exists {
		taskErr = c.tasks.Write(c.beforeTask.xml)
	} else {
		taskErr = c.tasks.Delete()
	}
	if c.beforeRun.exists {
		runErr = c.legacy.Write(c.beforeRun)
	} else {
		runErr = c.legacy.Delete()
	}
	err := errors.Join(taskErr, runErr)
	if err == nil {
		c.applied = false
	}
	return err
}
