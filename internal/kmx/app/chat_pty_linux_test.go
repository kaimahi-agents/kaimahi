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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
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
		{name: "hint submit", keys: "/li\t\r", want: "/lift", width: 10},
		{name: "transient backend hint", keys: "hello\r", want: "hello", width: 40},
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
			if tc.name == "transient backend hint" {
				r.ui = cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: tc.width})
				r.promptHint = "/help  /retry  /exit"
			}
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
			if tc.name == "transient backend hint" {
				wantRows = r.ui.UserMessage(tc.want, tc.width-1)
			}
			for index := range wantRows {
				wantRows[index] = strings.TrimRight(wantRows[index], " ")
			}
			if !strings.Contains(screen, strings.Join(wantRows, "\n")+"\nEND") {
				t.Fatalf("bad final screen:\n%s\nraw: %q", screen, output)
			}
			if (tc.name == "long user submission" || tc.name == "wrapped delete") && strings.Count(screen, "YOU >") != 1 {
				t.Fatalf("submission duplicated prompt/input: %s", screen)
			}
			if tc.name == "transient backend hint" && strings.Contains(screen, "/help  /retry  /exit") {
				t.Fatalf("transient slash hints remained under submitted input: %s", screen)
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
		{"hint height change", "/li", 20, 8, false},
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
					// Signals (including Go runtime preemption) can interrupt poll
					// without a terminal failure. Retry within the original deadline.
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
					if err == unix.EINTR {
						continue
					}
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
			deadline := time.Now().Add(2 * time.Second)
			for {
				n, err := unix.Poll(fds, 20)
				if err == unix.EINTR && time.Now().Before(deadline) {
					continue
				}
				if err != nil || time.Now().After(deadline) {
					t.Fatalf("PTY drain failed: %v %q", err, captured.String())
				}
				if n == 0 {
					break
				}
				var buf [8192]byte
				n, err = unix.Read(int(master.Fd()), buf[:])
				if err == unix.EINTR {
					continue
				}
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

func TestQuickstartChatHandoffWaitsForTerminalSizeToSettle(t *testing.T) {
	_, slave := chatPTY(t, 80)
	done := make(chan error, 1)
	go func() { done <- waitForStableTerminalSize(slave, slave) }()
	time.Sleep(30 * time.Millisecond)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 28, Col: 90}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal handoff did not stabilize")
	}
	width, height, err := term.GetSize(int(slave.Fd()))
	if err != nil || width != 100 || height != 30 {
		t.Fatalf("stable dimensions=%dx%d err=%v", width, height, err)
	}
}

func TestQuickstartChatHandoffDoesNotTripFirstRawPrompt(t *testing.T) {
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	done := make(chan error, 1)
	go func() {
		if err := waitForStableTerminalSize(slave, slave); err != nil {
			done <- err
			return
		}
		r.prompt()
		_, err := readSlashLine(context.Background(), slave, slave, r, false)
		done <- err
	}()
	time.Sleep(40 * time.Millisecond)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 28, Col: 90}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := io.WriteString(master, "/exit\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first prompt failed after handoff: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt did not accept input after handoff")
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
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Arguments:") })
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
				frameTitle := "DECISION"
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(ansi.Strip(s), "─ "+frameTitle+" ") })
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
				if !strings.Contains(ansi.Strip(static), "Call ID: exact") || !strings.Contains(screen, "resumed") || strings.Count(screen, "╭") != 0 {
					t.Fatalf("native editing damaged passive details or left a focus border:\n%s", screen)
				}
				if strings.Contains(screen, "YOU >") || r.transient {
					t.Fatalf("native prompt/progress state changed: %s", screen)
				}
			})
		}
	}
}

func TestChatPTYSpinnerResizeNeverErasesReflowedRows(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	r.verbose = true
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

func TestChatPTYConversationIsATopToBottomTimeline(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, width := range []int{24, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			master, slave := chatPTY(t, width)
			r := newChatRenderer(slave)
			var captured strings.Builder
			for turn, message := range []string{"first message", "second message"} {
				boundary := captured.Len()
				r.prompt()
				done := make(chan error, 1)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				go func() {
					line, err := readSlashLine(ctx, slave, slave, r, false)
					if err == nil && line != message {
						err = fmt.Errorf("message changed: %q", line)
					}
					done <- err
				}()
				chatPTYReadUntil(t, master, &captured, func(s string) bool {
					return strings.Contains(s[boundary:], "─ MESSAGE ")
				})
				if _, err := io.WriteString(master, message+"\r"); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				r.assistant("agent", fmt.Sprintf("reply %d", turn+1), true)
				r.responseTime(time.Duration(turn+2) * time.Second)
				r.finish()
				io.WriteString(slave, fmt.Sprintf("TURN %d\n", turn))
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, fmt.Sprintf("TURN %d\r\n", turn)) })
			}
			io.WriteString(slave, "END\n")
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
			screen := chatScreen(captured.String(), width)
			if strings.Count(screen, "╭─ YOU ") != 2 || strings.Count(screen, "╰") != 2 || strings.Contains(screen, "MESSAGE") || strings.Contains(screen, "  | ") {
				t.Fatalf("user boxes/agent background are inconsistent:\n%s", screen)
			}
			previous := -1
			for _, want := range []string{"first message", "reply 1", "Responded in 2s", "second message", "reply 2", "Responded in 3s", "END"} {
				index := strings.Index(screen, want)
				if index <= previous {
					t.Fatalf("missing or reordered %q:\n%s", want, screen)
				}
				previous = index
			}
		})
	}
}
