//go:build gui

package gui

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"unicode/utf16"
)

const autostartTaskName = "SigilDNS"

// autostartTaskTemplate is the logon task: Task Scheduler's "at log on of
// specific user", which the schtasks flags cannot express, with this user as
// the principal and InteractiveToken so the task runs only in that user's
// session and stores no password.  The XML also allows starting on battery and
// removes the default 72-hour run limit, neither of which schtasks flags can
// set.  %s: user id (principal and trigger), command.
const autostartTaskTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>--tray</Arguments>
    </Exec>
  </Actions>
</Task>
`

// autostartTaskXML renders the task definition for this user and exe.
func autostartTaskXML(exe, user string) string {
	id := xmlText(user)

	return fmt.Sprintf(autostartTaskTemplate, id, id, xmlText(quoteExe(exe)))
}

// quoteExe quotes the program path so a path with spaces stays one token.
func quoteExe(exe string) string { return `"` + exe + `"` }

// xmlText escapes a value for XML element text.
func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}

// utf16LE encodes the XML as UTF-16LE with a BOM, as its declaration requires.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for i, r := range u {
		b[2+2*i] = byte(r)
		b[3+2*i] = byte(r >> 8)
	}

	return b
}
