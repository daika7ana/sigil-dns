//go:build gui

package gui

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// taskDoc is the subset of the task XML the tests assert on.
type taskDoc struct {
	Principals []struct {
		Principal struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstancesPolicy string `xml:"MultipleInstancesPolicy"`
		ExecutionTimeLimit      string `xml:"ExecutionTimeLimit"`
	} `xml:"Settings"`
	Triggers []struct {
		Logon struct {
			Enabled bool   `xml:"Enabled"`
			UserID  string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Actions []struct {
		Exec struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// parseTaskXML decodes the generated document.  The declaration says UTF-16 but
// the string is UTF-8 here (the bytes handed to schtasks are converted by
// utf16LE), so the charset reader passes the ASCII content through.
func parseTaskXML(t *testing.T, doc string) taskDoc {
	t.Helper()

	d := xml.NewDecoder(strings.NewReader(doc))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }

	var got taskDoc
	if err := d.Decode(&got); err != nil {
		t.Fatalf("task XML does not parse: %v", err)
	}

	return got
}

func TestQuoteExe(t *testing.T) {
	got := quoteExe(`C:\Program Files\SigilDNS\sigildns-gui.exe`)
	want := `"C:\Program Files\SigilDNS\sigildns-gui.exe"`
	if got != want {
		t.Errorf("quoteExe = %q, want %q", got, want)
	}
}

// TestAutostartTaskXML pins the definition: the logon trigger and the principal
// both name this user (Task Scheduler's "at log on of specific user"), with an
// interactive token at highest privileges, plus the settings flags cannot
// express.
func TestAutostartTaskXML(t *testing.T) {
	const sid = `S-1-5-21-629688585-678344646-3373423834-1001`
	exe := `C:\Program Files\SigilDNS\sigildns-gui.exe`
	doc := autostartTaskXML(exe, sid)
	got := parseTaskXML(t, doc)

	if len(got.Principals) != 1 || len(got.Actions) != 1 || len(got.Triggers) != 1 {
		t.Fatalf("principals/actions/triggers = %d/%d/%d, want 1 each",
			len(got.Principals), len(got.Actions), len(got.Triggers))
	}

	p := got.Principals[0].Principal
	if p.UserID != sid || p.LogonType != "InteractiveToken" || p.RunLevel != "HighestAvailable" {
		t.Errorf("principal = %+v", p)
	}
	if !got.Triggers[0].Logon.Enabled {
		t.Error("logon trigger is not enabled")
	}
	if got.Triggers[0].Logon.UserID != sid {
		t.Errorf("logon trigger user = %q, want %q", got.Triggers[0].Logon.UserID, sid)
	}
	if strings.Contains(doc, "<StartBoundary>") {
		t.Error("logon trigger carries a StartBoundary; it should be a pure logon trigger")
	}

	for _, want := range []string{
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("task XML lacks %s", want)
		}
	}
	if got.Settings.ExecutionTimeLimit != "PT0S" {
		t.Errorf("ExecutionTimeLimit = %q, want PT0S", got.Settings.ExecutionTimeLimit)
	}
	if got.Settings.MultipleInstancesPolicy != "IgnoreNew" {
		t.Errorf("MultipleInstancesPolicy = %q, want IgnoreNew", got.Settings.MultipleInstancesPolicy)
	}

	e := got.Actions[0].Exec
	if e.Command != `"`+exe+`"` || e.Arguments != "--tray" {
		t.Errorf("action = %q %q", e.Command, e.Arguments)
	}
}

// TestAutostartTaskElementOrder keeps the settings in the order the Task
// Scheduler schema requires; out of order, schtasks rejects the file, and that
// cannot be exercised on a non-Windows host.
func TestAutostartTaskElementOrder(t *testing.T) {
	doc := autostartTaskXML(`C:\x.exe`, `WS\ada`)

	last := -1
	for _, tag := range []string{
		"<DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>",
		"<MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>",
	} {
		i := strings.Index(doc, tag)
		if i <= last {
			t.Errorf("%s is missing or out of schema order", tag)
		}
		last = i
	}
}

// TestAutostartTaskXMLEscapes proves a user or path containing XML
// metacharacters still produces a parseable task with the original values.
func TestAutostartTaskXMLEscapes(t *testing.T) {
	got := parseTaskXML(t, autostartTaskXML(`C:\A & B\x.exe`, `WS\a&b<c`))

	if p := got.Principals[0].Principal.UserID; p != `WS\a&b<c` {
		t.Errorf("UserId = %q", p)
	}
	if u := got.Triggers[0].Logon.UserID; u != `WS\a&b<c` {
		t.Errorf("trigger UserId = %q", u)
	}
	if c := got.Actions[0].Exec.Command; c != `"C:\A & B\x.exe"` {
		t.Errorf("Command = %q", c)
	}
}

func TestUTF16LE(t *testing.T) {
	s := `C:\Users\Müller\sigildns-gui.exe`
	b := utf16LE(s)

	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("missing UTF-16LE BOM: % x", b[:2])
	}
	if len(b) != 2+2*len(utf16.Encode([]rune(s))) {
		t.Fatalf("encoded length = %d", len(b))
	}

	u := make([]uint16, 0, (len(b)-2)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(u)); got != s {
		t.Errorf("decoded = %q, want %q", got, s)
	}
}
