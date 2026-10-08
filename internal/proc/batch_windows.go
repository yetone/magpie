package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

// batch has a .cmd or .bat (npm.cmd, a CLI's npm shim) run by cmd.exe with
// a command line cmd.exe reads back as the arguments given. Windows runs a
// batch file as `cmd.exe /c <command line>`, and Go builds that line for a
// program's own parsing: cmd.exe, seeing more than two quotes, drops the
// first and the last, so `"C:\Program Files\nodejs\npm.cmd" … --prefix
// "C:\Program Files\nodejs" …` ran C:\Program; and an argument with & or |
// in it, which Go leaves unquoted, was split into a second command there.
//
// Each argument is quoted the way Rust's standard library quotes one for a
// batch file: in quotes when it has anything but letters, digits and
// #$*+-./:?@\_ in it or a \ at its end, a " doubled, a % broken up so
// cmd.exe expands no variable, and a line break refused, as it would end
// the command. A doubled " is read back as one by programs built with
// Microsoft's C runtime since 2008, node among them; Go's own parser reads
// it the older way.
func batch(cmd *exec.Cmd) {
	if cmd.Err != nil || cmd.Path == "" {
		return
	}
	switch strings.ToLower(filepath.Ext(cmd.Path)) {
	case ".cmd", ".bat":
	default:
		return
	}
	if strings.ContainsAny(cmd.Path, "\"\r\n") {
		cmd.Err = errBatchArg
		return
	}
	var b strings.Builder
	b.WriteString(`/d /e:ON /v:OFF /s /c ""`)
	b.WriteString(cmd.Path)
	b.WriteByte('"')
	for _, a := range cmd.Args[1:] {
		if strings.ContainsAny(a, "\r\n\x00") {
			cmd.Err = errBatchArg
			return
		}
		b.WriteByte(' ')
		quoteBatchArg(&b, a)
	}
	b.WriteByte('"')
	shell := comSpec()
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = `"` + shell + `" ` + b.String()
	cmd.Args = append([]string{shell, "/d", "/e:ON", "/v:OFF", "/s", "/c", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = shell
}

var errBatchArg = errors.New("cmd.exe can't be handed a batch file with a quote in its path, or a line break or NUL in an argument")

func quoteBatchArg(b *strings.Builder, a string) {
	// a trailing \ is quoted too, or a script's "%~1" would have it
	// escape the quote after it
	quote := a == "" || strings.HasSuffix(a, `\`) || strings.IndexFunc(a, func(r rune) bool {
		if r < 0x80 {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`#$*+-./:?@\_`, r))
		}
		return unicode.IsControl(r)
	}) >= 0
	if quote {
		b.WriteByte('"')
	}
	backslashes := 0
	for _, r := range a {
		switch r {
		case '\\':
			backslashes++
			b.WriteRune(r)
			continue
		case '"':
			// the backslashes before it are doubled and the quote is
			// doubled, which keeps cmd.exe's idea of what is quoted
			b.WriteString(strings.Repeat(`\`, backslashes))
			b.WriteByte('"')
		case '%':
			// %cd:~,% is always empty, and it stands between this % and
			// the name after it
			b.WriteString("%%cd:~,")
		}
		backslashes = 0
		b.WriteRune(r)
	}
	if quote {
		b.WriteString(strings.Repeat(`\`, backslashes))
		b.WriteByte('"')
	}
}

// comSpec is cmd.exe: %ComSpec% when it names one, else the one in
// System32.
func comSpec() string {
	if c := os.Getenv("ComSpec"); filepath.IsAbs(c) && strings.EqualFold(filepath.Base(c), "cmd.exe") {
		return c
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "cmd.exe")
}
