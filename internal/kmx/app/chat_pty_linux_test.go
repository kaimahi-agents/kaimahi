package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

func chatPTY(t *testing.T, width int) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: uint16(width)}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

// A small screen model, rather than stripping ANSI: erasure and wrapping are
// precisely the behavior these regressions need to observe.
func chatScreen(text string, width int) string {
	rows := [][]rune{make([]rune, width)}
	x, y := 0, 0
	ensure := func() {
		for len(rows) <= y {
			rows = append(rows, make([]rune, width))
		}
	}
	for p := 0; p < len(text); p++ {
		c := text[p]
		if c == '\x1b' && p+1 < len(text) && text[p+1] == '[' {
			end := p + 2
			for end < len(text) && (text[end] < '@' || text[end] > '~') {
				end++
			}
			if end == len(text) {
				break
			}
			n, _ := strconv.Atoi(text[p+2 : end])
			if n == 0 {
				n = 1
			}
			switch text[end] {
			case 'A':
				y = max(0, y-n)
			case 'B':
				y += n
				ensure()
			case 'C':
				x = min(width-1, x+n)
			case 'K':
				rows[y] = make([]rune, width)
			}
			p = end
			continue
		}
		switch c {
		case '\r':
			x = 0
		case '\n':
			y++
			ensure()
		default:
			if c < 32 {
				continue
			}
			if x >= width {
				x = 0
				y++
				ensure()
			}
			value, size := utf8.DecodeRuneInString(text[p:])
			p += size - 1
			rows[y][x] = value
			x++
		}
	}
	var lines []string
	for _, row := range rows {
		for i := range row {
			if row[i] == 0 {
				row[i] = ' '
			}
		}
		lines = append(lines, strings.TrimRight(string(row), " "))
	}
	return strings.Join(lines, "\n")
}

func TestChatPTYRawInput(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, keys, want string
		width                    int
		height                   uint16
		cancel, eof, pause       bool
	}{
		{name: "question", prompt: "Answer:", keys: "hello\r", want: "hello", width: 40},
		{name: "approval", prompt: "Approve? [y/N]:", keys: "yes\r", want: "yes", width: 40},
		{name: "reason", prompt: "Rejection reason (optional):", keys: "no thanks\r", want: "no thanks", width: 12},
		{name: "wrapped delete", keys: "abcdefghijklmnop\x7f\x7f\r", want: "abcdefghijklmn", width: 10},
		{name: "shrink wrapped input", keys: "abcdefghijklmnop" + strings.Repeat("\x7f", 16) + "x\r", want: "x", width: 10},
		{name: "hint submit", keys: "/hi\t\r", want: "/history", width: 10},
		{name: "tiny hint", keys: "/\x7fx\r", want: "x", width: 3},
		{name: "grapheme delete", keys: "e\u0301👩‍💻\x7f\x7fx\r", want: "x", width: 12},
		{name: "malformed utf8", keys: "\xc3x\r", want: "x", width: 12},
		{name: "navigation", keys: "\x1b[A\x1b[1~\x1b[3~\x1bOHx\r", want: "x", width: 20},
		{name: "escape ctrl-c", keys: "\x1b\x03", cancel: true, width: 20},
		{name: "csi ctrl-c", keys: "\x1b[1;\x03", cancel: true, width: 20},
		{name: "ctrl-d eof", keys: "\x04", eof: true, width: 20},
		{name: "escape timeout", keys: "\x1b", pause: true, want: "x", width: 20},
		{name: "csi timeout", keys: "\x1b[", pause: true, want: "x", width: 20},
		{name: "idle cancel", cancel: true, width: 20},
		{name: "escape cancel", keys: "\x1b[", cancel: true, width: 20},
		{name: "long user submission", keys: strings.Repeat("0123456789", 30) + "\r", want: strings.Repeat("0123456789", 30), width: 40, height: 5},
		{name: "long native submission", prompt: "Answer:", keys: strings.Repeat("abcdefghij", 30) + "\r", want: strings.Repeat("abcdefghij", 30), width: 40, height: 5},
		{name: "long cancelled input", keys: strings.Repeat("0123456789", 30) + "\x03", cancel: true, width: 40, height: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := chatPTY(t, tc.width)
			if tc.height != 0 {
				if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: tc.height, Col: uint16(tc.width)}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			stop, captured := make(chan struct{}), make(chan string, 1)
			go func() {
				var out bytes.Buffer
				defer func() { captured <- out.String() }()
				for {
					fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
					if n, _ := unix.Poll(fds, 20); n > 0 {
						var buf [8192]byte
						n, err := unix.Read(int(master.Fd()), buf[:])
						if err != nil {
							return
						}
						out.Write(buf[:n])
						continue
					}
					select {
					case <-stop:
						return
					default:
					}
				}
			}()
			t.Cleanup(func() { close(stop) })
			r := &chatRenderer{out: slave, cursor: true}
			if tc.prompt != "" {
				r.operationPrompt("NATIVE", colorYellow, "payload", tc.prompt)
			} else {
				r.prompt()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				line string
				err  error
			}
			done := make(chan result, 1)
			go func() { line, err := readSlashLine(ctx, slave, slave, r, tc.prompt == ""); done <- result{line, err} }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				state, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
				if err != nil {
					t.Fatal(err)
				}
				if state.Lflag&unix.ICANON == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("raw mode did not start")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := io.WriteString(master, tc.keys); err != nil {
				t.Fatal(err)
			}
			if tc.pause {
				time.Sleep(150 * time.Millisecond)
				io.WriteString(master, "x\r")
			}
			if tc.cancel && !strings.Contains(tc.keys, "\x03") {
				// Let escape bytes reach the raw reader before cancelling it;
				// otherwise the restored canonical terminal may echo queued input.
				if tc.keys != "" {
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
			}
			select {
			case got := <-done:
				if got.line != tc.want || (tc.eof && got.err != io.EOF) || (tc.cancel && got.err != context.Canceled) || (!tc.eof && !tc.cancel && got.err != nil) {
					t.Fatalf("got %q, %v", got.line, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("raw input blocked")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("terminal not restored: %v", err)
			}
			// A marker also proves submission returned to column zero below input.
			io.WriteString(slave, "END\n")
			// Drain output without closing the slave, which can lose queued PTY data.
			stop <- struct{}{}
			output := <-captured
			screen := chatScreen(output, tc.width)
			if tc.height != 0 {
				if !strings.Contains(output, "[... input omitted ...]") {
					t.Fatalf("clipped editing input was not marked: %q", output)
				}
				if tc.eof || tc.cancel {
					if !strings.Contains(screen, "[... input omitted ...]") {
						t.Fatalf("cancel unexpectedly printed complete input: %s", screen)
					}
					return
				}
				if strings.Contains(screen, "input omitted") {
					t.Fatalf("submission retained clipped viewport: %s", screen)
				}
			}
			prefix := "YOU > "
			if tc.prompt != "" {
				prefix = "  " + tc.prompt + " "
			}
			wantRows := chatInputRows(prefix, tc.want, tc.width)
			for index := range wantRows {
				wantRows[index] = strings.TrimRight(wantRows[index], " ")
			}
			if !strings.Contains(screen, strings.Join(wantRows, "\n")+"\nEND") {
				t.Fatalf("bad final screen:\n%s\nraw: %q", screen, output)
			}
			if (tc.name == "long user submission" || tc.name == "wrapped delete") && strings.Count(screen, "YOU >") != 1 {
				t.Fatalf("submission duplicated prompt/input: %s", screen)
			}
			if tc.prompt != "" && strings.Contains(screen, "YOU >") {
				t.Fatalf("native prompt replaced: %s", screen)
			}
		})
	}
}

func TestChatPTYTerminalAndInputPolicy(t *testing.T) {
	_, slave := chatPTY(t, 40)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if !isTerminal(slave) || !isInteractiveTerminal(slave) {
		t.Fatal("PTY not recognized")
	}
	if !newChatRenderer(slave).cursor || !newChatInput(nil, slave, slave, nil).enhanced {
		t.Fatal("PTY capabilities missing")
	}
	t.Setenv("NO_COLOR", "1")
	if newChatRenderer(slave).cursor || newChatInput(nil, slave, slave, nil).enhanced {
		t.Fatal("NO_COLOR input policy changed")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if newChatRenderer(slave).cursor || newChatInput(nil, slave, slave, nil).enhanced {
		t.Fatal("dumb terminal enhanced")
	}
}

func TestChatPTYResizeAbortsWithoutStaleRedraw(t *testing.T) {
	for _, tc := range []struct {
		name, input   string
		width, height uint16
		native        bool
	}{
		{"idle shrink", "", 12, 24, false},
		{"wrapped grow", "abcdefghijklmnopqrstuv", 40, 24, false},
		{"hint height change", "/hi", 20, 8, false},
		{"approval", "y", 12, 24, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := chatPTY(t, 20)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			r := &chatRenderer{out: slave, cursor: true}
			r.block("HISTORY", colorBlue, "durable")
			if tc.native {
				r.operationPrompt("NATIVE APPROVAL", colorYellow, "Tool: delete", "Approve? [y/N]:")
			} else {
				r.prompt()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				line string
				err  error
			}
			done := make(chan result, 1)
			go func() { line, err := readSlashLine(ctx, slave, slave, r, !tc.native); done <- result{line, err} }()
			// Read until the initial raw redraw has actually reached the PTY.
			var captured strings.Builder
			readUntil := func(ready func(string) bool) {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for !ready(captured.String()) {
					fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
					n, err := unix.Poll(fds, 20)
					if err != nil || time.Now().After(deadline) {
						t.Fatalf("PTY output timeout: %v %q", err, captured.String())
					}
					if n == 0 {
						continue
					}
					var buf [8192]byte
					n, err = unix.Read(int(master.Fd()), buf[:])
					if err != nil {
						t.Fatal(err)
					}
					captured.Write(buf[:n])
				}
			}
			readUntil(func(s string) bool { return strings.Contains(s, "\x1b[2K") })
			if _, err := io.WriteString(master, tc.input); err != nil {
				t.Fatal(err)
			}
			if tc.input != "" {
				readUntil(func(s string) bool {
					return strings.Contains(strings.ReplaceAll(chatScreen(s, 20), "\n", ""), tc.input)
				})
			}
			// Allow completed writes to drain before marking the resize boundary.
			fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
			for {
				n, _ := unix.Poll(fds, 20)
				if n == 0 {
					break
				}
				var buf [8192]byte
				n, err := unix.Read(int(master.Fd()), buf[:])
				if err != nil {
					t.Fatal(err)
				}
				captured.Write(buf[:n])
			}
			boundary := captured.Len()
			if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: tc.height, Col: tc.width}); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.line != "" || got.err != errChatResized {
					t.Fatalf("resize submitted input: %q %v", got.line, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("idle resize did not abort")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after || r.promptOpen {
				t.Fatalf("resize did not restore terminal/prompt: %v", err)
			}
			io.WriteString(slave, "END\n")
			readUntil(func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
			suffix := captured.String()[boundary:]
			if suffix != "\r\n\r\nEND\r\n" {
				t.Fatalf("resize used stale cursor coordinates: %q", suffix)
			}
			if !strings.Contains(captured.String(), "durable") {
				t.Fatal("history lost")
			}
		})
	}
}

func TestChatPTYSpinnerUsesRendererDestination(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 20)
	dir := t.TempDir()
	fakeTool(t, dir, "kagent", `sleep 0.4
printf '%s\n' '{"status":{"state":"completed"},"artifact":{"parts":[{"kind":"text","text":"first"}]}}'
printf '%s\n' '{"artifact":{"parts":[{"kind":"text","text":" second"}]}}'`)
	var stderr bytes.Buffer
	a := &App{Out: slave, Err: &stderr}
	r := newChatRenderer(slave)
	r.beginAssistant("agent")
	if _, err := a.invokeStream(context.Background(), dir+"/kagent", "", "agent", "hello", "", "off", r, nil); err != nil {
		t.Fatal(err)
	}
	r.finish()
	if _, err := io.WriteString(slave, "END\n"); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	var captured strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(captured.String(), "END\r\n") {
		if n, err := unix.Poll(fds, 100); err != nil || time.Now().After(deadline) {
			t.Fatalf("incomplete terminal output: %v: %q", err, captured.String())
		} else if n == 0 {
			continue
		}
		var buf [8192]byte
		n, err := unix.Read(int(master.Fd()), buf[:])
		if err != nil {
			t.Fatal(err)
		}
		captured.Write(buf[:n])
	}
	output := captured.String()
	if !strings.Contains(output, "WORKING") || stderr.Len() != 0 {
		t.Fatalf("spinner used stderr capabilities/destination: %q / %q", output, stderr.String())
	}
	screen := chatScreen(output, 20)
	if strings.Contains(screen, "WORKING") || !strings.Contains(screen, "  | first second") {
		t.Fatalf("spinner or chunks damaged screen:\n%s\nraw: %q", screen, output)
	}
	// A terminal stderr must never enable a spinner in a plain stdout transcript.
	var plain bytes.Buffer
	a.Out, a.Err = &plain, slave
	r = newChatRenderer(&plain)
	if _, err := a.invokeStream(context.Background(), dir+"/kagent", "", "agent", "hello", "", "off", r, nil); err != nil {
		t.Fatal(err)
	}
	r.finish()
	if plain.String() != "AGENT (agent)\n  | first second\n\n" {
		t.Fatalf("plain transcript changed: %q", plain.String())
	}
}

func chatPTYReadUntil(t *testing.T, master *os.File, captured *strings.Builder, ready func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready(captured.String()) {
		fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 20)
		if err == unix.EINTR && time.Now().Before(deadline) {
			continue
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("PTY output timeout: %v %q", err, captured.String())
		}
		if n == 0 {
			continue
		}
		var buf [8192]byte
		n, err = unix.Read(int(master.Fd()), buf[:])
		if err != nil {
			t.Fatal(err)
		}
		captured.Write(buf[:n])
	}
}

func TestChatPTYStaticCalloutPromptTransitions(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, width := range []int{20, 100} {
		for _, prompt := range []string{"Approve? [y/N]:", "Answer:", "Rejection reason (optional):"} {
			t.Run(fmt.Sprintf("%d/%s", width, prompt), func(t *testing.T) {
				master, slave := chatPTY(t, width)
				r := newChatRenderer(slave)
				r.assistant("agent", "durable", true)
				r.operationPrompt("NATIVE APPROVAL", colorYellow, "Call ID: exact\nTool: delete\nArguments:\n{\"name\":\"pod-a\"}", prompt)
				var captured strings.Builder
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, prompt) })
				static := captured.String()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					line, err := readSlashLine(ctx, slave, slave, r, false)
					if err == nil && line != "yes" {
						err = fmt.Errorf("answer changed: %q", line)
					}
					done <- err
				}()
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "\x1b[2K") })
				io.WriteString(master, "yes\r")
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				r.working("Waiting after decision")
				r.assistant("agent", "resumed", true)
				r.finish()
				io.WriteString(slave, "END\n")
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
				screen := chatScreen(captured.String(), width)
				staticScreen := chatScreen(static, width)
				borderEnd := strings.LastIndex(staticScreen, "╯")
				if borderEnd < 0 || !strings.HasPrefix(screen, staticScreen[:borderEnd+len("╯")]) || !strings.Contains(screen, "resumed") || strings.Contains(captured.String()[len(static):], "╭") {
					t.Fatalf("native editing damaged or repainted durable callout:\n%s", screen)
				}
				if strings.Contains(screen, "YOU >") || r.transient {
					t.Fatalf("native prompt/progress state changed: %s", screen)
				}
			})
		}
	}
}

func TestChatPTYHelpAndExitDispatch(t *testing.T) {
	for _, tc := range []struct{ name, keys, reason string }{
		{"exit", "/exit\r", "exit requested"},
		{"eof", "\x04", "end of input"},
		{"cancel", "\x03", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			a := chatUXFixture(t)
			master, slave := chatPTY(t, 100)
			a.Out, a.Stdin = slave, slave
			done := make(chan error, 1)
			go func() { done <- a.interactiveChat("/must-not-invoke", "agent", "", "") }()
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "\x1b[2K") })
			io.WriteString(master, "/he\t\r")
			chatPTYReadUntil(t, master, &captured, func(s string) bool {
				_, after, found := strings.Cut(s, "[CHAT HELP]")
				return found && strings.Contains(after, "\x1b[2K")
			})
			io.WriteString(master, tc.keys)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("chat did not exit")
			}
			io.WriteString(slave, "END\n")
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
			text := ansi.Strip(captured.String())
			if !strings.Contains(text, "Chat ended ("+tc.reason+").") || !strings.Contains(text, "Session:") || !strings.Contains(text, "Connecting to agent") || !strings.Contains(text, "Checking model and tool posture") {
				t.Fatalf("chat dispatch/startup: %s", text)
			}
		})
	}
}

func TestChatPTYSpinnerContinuesAfterToolEvent(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, mode := range []string{"off", "summary"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := chatPTY(t, 80)
			dir := t.TempDir()
			fakeTool(t, dir, "kagent", `printf '%s\n' '{"status":{"state":"working","message":{"role":"agent","parts":[{"kind":"text","text":"checking"},{"kind":"data","metadata":{"kagent_type":"function_call"},"data":{"id":"one","name":"read","args":{}}}]}}}'
sleep 0.6
printf '%s\n' '{"status":{"state":"completed"},"artifact":{"parts":[{"kind":"text","text":"finished"}]}}'`)
			r := newChatRenderer(slave)
			a := &App{Out: slave}
			if _, err := a.invokeStream(context.Background(), dir+"/kagent", "", "agent", "hello", "", mode, r, nil); err != nil {
				t.Fatal(err)
			}
			r.finish()
			io.WriteString(slave, "END\n")
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
			text := ansi.Strip(captured.String())
			if !strings.Contains(text, "WORKING agent") || r.transient {
				t.Fatalf("missing/stale post-tool spinner: %q", text)
			}
			screen := chatScreen(captured.String(), 80)
			if strings.Contains(screen, "WORKING agent") || !strings.Contains(screen, "checking") || !strings.Contains(screen, "finished") {
				t.Fatalf("post-tool spinner damaged transcript: %s", screen)
			}
		})
	}
}

func TestChatPTYSpinnerResizeNeverErasesReflowedRows(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	r.block("HISTORY", colorBlue, "durable")
	r.spinner("agent", "|", time.Second)
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "1s") })
	boundary := captured.Len()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 10}); err != nil {
		t.Fatal(err)
	}
	r.clearTransient()
	r.spinner("agent", "/", 2*time.Second)
	r.assistant("agent", "reply", true)
	r.finish()
	io.WriteString(slave, "END\n")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
	if strings.Contains(captured.String()[boundary:], "\x1b[2K") || r.transient || !r.spinnerDisabled {
		t.Fatalf("resize erased old rows or restarted stale spinner: %q", captured.String())
	}
}

func TestChatPTYNoColorKeepsScannerTranscript(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	a := chatUXFixture(t)
	master, slave := chatPTY(t, 100)
	a.Out, a.Stdin = slave, slave
	done := make(chan error, 1)
	go func() { done <- a.interactiveChat("/must-not-invoke", "agent", "", "") }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "YOU > ") })
	io.WriteString(master, "/help\n/exit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scanner chat did not exit")
	}
	io.WriteString(slave, "END\n")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
	text := captured.String()
	if !strings.HasPrefix(text, "CHAT STATUS\r\n------------\r\n  Agent: agent") || !strings.Contains(text, "[CHAT HELP]") || !strings.Contains(text, "Status: ended") || strings.Contains(text, "\x1b") || strings.Contains(text, "WORKING") {
		t.Fatalf("NO_COLOR scanner transcript changed: %q", text)
	}
}
