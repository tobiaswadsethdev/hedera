package hooks

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Registration is how one hook event is registered.
type Registration struct {
	Event string
	// Async hooks run in the background and never delay Claude Code. Used
	// for events that only nudge the status and may fire often.
	Async bool
	// Timeout in seconds; 0 leaves Claude Code's default. SessionEnd gets
	// none, because a per-hook timeout raises the budget for every SessionEnd
	// hook the user has.
	Timeout int
}

// Registrations lists the events hedera listens to. Keep in sync with
// session.HookNames.
var Registrations = []Registration{
	{Event: "SessionStart", Timeout: 5},
	{Event: "UserPromptSubmit", Timeout: 5},
	{Event: "PermissionRequest", Async: true},
	{Event: "PostToolUse", Async: true},
	{Event: "PostToolUseFailure", Async: true},
	{Event: "Notification", Timeout: 5},
	{Event: "Stop", Timeout: 5},
	{Event: "StopFailure", Timeout: 5},
	{Event: "SessionEnd"},
}

// Invocation is how a hook handler runs `<exe> hook`.
type Invocation struct {
	Command string
	Args    []string // set for exec form, which runs without a shell
}

// Command returns the shell-form command that runs `<exe> hook`. It works on
// every Claude Code version on macOS and Linux.
func Command(exe string) string {
	return shellQuote(exe) + " hook"
}

// InvocationFor picks the form for an OS. Windows uses exec form, because
// Claude Code may run shell-form hooks through PowerShell or Git Bash there,
// which quote differently.
func InvocationFor(exe, goos string) Invocation {
	if goos == "windows" {
		return Invocation{Command: exe, Args: []string{"hook"}}
	}
	return Invocation{Command: Command(exe)}
}

func shellQuote(s string) string {
	safe := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%", r))
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsOurs reports whether a hook handler object was installed by hedera: a
// command hook running `<…/hedera> hook`, in shell form or exec form.
func IsOurs(handler *Value) bool {
	if handler == nil || handler.Obj == nil {
		return false
	}
	if t, _ := handler.Obj.Get("type").Str(); t != "command" {
		return false
	}
	cmd, ok := handler.Obj.Get("command").Str()
	if !ok {
		return false
	}
	words := []string{cmd}
	if args := handler.Obj.Get("args"); args != nil && args.IsArr {
		for _, a := range args.Arr {
			s, _ := a.Str()
			words = append(words, s)
		}
	} else if words = shellWords(cmd); words == nil {
		return false
	}
	if len(words) != 2 || words[1] != "hook" {
		return false
	}
	base := filepath.Base(strings.ReplaceAll(words[0], `\`, "/"))
	return base == "hedera" || base == "hedera.exe"
}

// shellWords splits a simple POSIX shell command into words, handling
// quotes and backslashes. It returns nil for anything with shell syntax
// beyond that (pipes, variables, ;), which can't be ours.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				} else if s[i] == '$' || s[i] == '`' {
					return nil
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil
			}
			inWord = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case strings.IndexByte("|&;<>()$`*?[#~=", c) >= 0:
			return nil
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// ErrNotObject means a part of the settings file has an unexpected shape, so
// it is left alone rather than rewritten.
var ErrNotObject = errors.New("unexpected settings structure")

func hooksObject(root *Value, create bool) (*Object, error) {
	if root.Obj == nil {
		return nil, fmt.Errorf("%w: the top level is not a JSON object", ErrNotObject)
	}
	h := root.Obj.Get("hooks")
	if h == nil {
		if !create {
			return nil, nil
		}
		h = NewObject()
		root.Obj.Set("hooks", h)
	}
	if h.Obj == nil {
		return nil, fmt.Errorf("%w: \"hooks\" is not an object", ErrNotObject)
	}
	return h.Obj, nil
}

// remove deletes hedera's handlers everywhere under hooks, dropping matcher
// groups and events that become empty. Groups it doesn't understand are kept.
func remove(h *Object) error {
	for _, event := range append([]string(nil), h.Keys...) {
		groups := h.Get(event)
		if !groups.IsArr {
			return fmt.Errorf("%w: hooks.%s is not an array", ErrNotObject, event)
		}
		var keptGroups []*Value
		for _, g := range groups.Arr {
			handlers := (*Value)(nil)
			if g.Obj != nil {
				handlers = g.Obj.Get("hooks")
			}
			if handlers == nil || !handlers.IsArr {
				keptGroups = append(keptGroups, g)
				continue
			}
			var kept []*Value
			for _, hd := range handlers.Arr {
				if !IsOurs(hd) {
					kept = append(kept, hd)
				}
			}
			if len(kept) == len(handlers.Arr) {
				keptGroups = append(keptGroups, g)
				continue
			}
			if len(kept) > 0 {
				handlers.Arr = kept
				keptGroups = append(keptGroups, g)
			}
		}
		if len(keptGroups) == 0 {
			h.Delete(event)
		} else {
			groups.Arr = keptGroups
		}
	}
	return nil
}

func handler(inv Invocation, r Registration) *Value {
	h := NewObject()
	h.Obj.Set("type", String("command"))
	h.Obj.Set("command", String(inv.Command))
	if inv.Args != nil {
		args := NewArray()
		for _, a := range inv.Args {
			args.Arr = append(args.Arr, String(a))
		}
		h.Obj.Set("args", args)
	}
	if r.Async {
		h.Obj.Set("async", Bool(true))
	}
	if r.Timeout > 0 {
		h.Obj.Set("timeout", Number(r.Timeout))
	}
	return h
}

// Install makes root contain exactly one hedera handler per registered
// event, running inv, and leaves everything else as it was.
func Install(root *Value, inv Invocation) error {
	h, err := hooksObject(root, true)
	if err != nil {
		return err
	}
	if err := remove(h); err != nil {
		return err
	}
	for _, r := range Registrations {
		group := NewObject()
		group.Obj.Set("hooks", NewArray(handler(inv, r)))
		if existing := h.Get(r.Event); existing != nil {
			existing.Arr = append(existing.Arr, group)
		} else {
			h.Set(r.Event, NewArray(group))
		}
	}
	return nil
}

// Uninstall removes every hedera handler from root.
func Uninstall(root *Value) error {
	h, err := hooksObject(root, false)
	if err != nil || h == nil {
		return err
	}
	if err := remove(h); err != nil {
		return err
	}
	if len(h.Keys) == 0 {
		root.Obj.Delete("hooks")
	}
	return nil
}

// Installed reports which registered events currently have a hedera handler.
func Installed(root *Value) []string {
	h, err := hooksObject(root, false)
	if err != nil || h == nil {
		return nil
	}
	var out []string
	for _, r := range Registrations {
		groups := h.Get(r.Event)
		if groups == nil || !groups.IsArr {
			continue
		}
	found:
		for _, g := range groups.Arr {
			if g.Obj == nil {
				continue
			}
			if hs := g.Obj.Get("hooks"); hs != nil && hs.IsArr {
				for _, hd := range hs.Arr {
					if IsOurs(hd) {
						out = append(out, r.Event)
						break found
					}
				}
			}
		}
	}
	return out
}

// HooksDisabled reports whether the settings turn all hooks off.
func HooksDisabled(root *Value) bool {
	if root.Obj == nil {
		return false
	}
	v := root.Obj.Get("disableAllHooks")
	b, ok := v.scalarBool()
	return ok && b
}

func (v *Value) scalarBool() (bool, bool) {
	if v == nil {
		return false, false
	}
	b, ok := v.Scalar.(bool)
	return b, ok
}
