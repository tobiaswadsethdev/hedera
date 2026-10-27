package service

import (
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTaskUnit(t *testing.T) {
	u := taskUnit(`C:\Users\t & co\bin\hedera.exe`, `DESKTOP\t`, `C:\Users\t\AppData\Roaming\hedera`)
	if !strings.HasSuffix(u.Path, "hedera-task.xml") {
		t.Fatal(u.Path)
	}
	// It must be well-formed XML with the escaped values in place.
	var task struct {
		Triggers struct {
			LogonTrigger struct {
				UserId string
			}
		}
		Principals struct {
			Principal struct {
				LogonType string
				RunLevel  string
			}
		}
		Actions struct {
			Exec struct {
				Command   string
				Arguments string
			}
		}
	}
	// encoding/xml refuses the UTF-16 declaration on a UTF-8 string.
	body := strings.Replace(u.Content, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(body), &task); err != nil {
		t.Fatal(err)
	}
	if task.Triggers.LogonTrigger.UserId != `DESKTOP\t` || task.Principals.Principal.LogonType != "InteractiveToken" || task.Principals.Principal.RunLevel != "LeastPrivilege" {
		t.Fatalf("task %+v", task)
	}
	if task.Actions.Exec.Command != `C:\Users\t & co\bin\hedera.exe` {
		t.Fatalf("command %q", task.Actions.Exec.Command)
	}
	if !strings.Contains(task.Actions.Exec.Arguments, `--log-file "C:\Users\t\AppData\Roaming\hedera`) || !strings.Contains(task.Actions.Exec.Arguments, "--detach-console") {
		t.Fatalf("arguments %q", task.Actions.Exec.Arguments)
	}
}

func TestUTF16LE(t *testing.T) {
	b := utf16LE("ä<")
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatal("no BOM")
	}
	u := make([]uint16, 0)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if string(utf16.Decode(u)) != "ä<" {
		t.Fatal("round trip")
	}
}
