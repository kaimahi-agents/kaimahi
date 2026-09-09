package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

type slashCommand struct {
	name  string
	usage string
}

var slashCommandList = []slashCommand{
	{"/exit", "/exit"},
	{"/govern", "/govern"},
	{"/help", "/help"},
	{"/history", "/history"},
	{"/new", "/new"},
	{"/resume", "/resume <id>"},
	{"/retry", "/retry"},
	{"/session", "/session"},
	{"/sessions", "/sessions"},
	{"/tools", "/tools off|summary|verbose"},
	{"/ungovern", "/ungovern"},
}

type slashTrie struct {
	children map[rune]*slashTrie
	matches  []int
}

func newSlashTrie(commands []slashCommand) *slashTrie {
	root := &slashTrie{children: map[rune]*slashTrie{}}
	for i, command := range commands {
		node := root
		for _, char := range command.name {
			next := node.children[char]
			if next == nil {
				next = &slashTrie{children: map[rune]*slashTrie{}}
				node.children[char] = next
			}
			node = next
			node.matches = append(node.matches, i)
		}
	}
	return root
}

func (t *slashTrie) find(prefix string) []slashCommand {
	node := t
	for _, char := range prefix {
		node = node.children[char]
		if node == nil {
			return nil
		}
	}
	commands := make([]slashCommand, 0, len(node.matches))
	for _, index := range node.matches {
		commands = append(commands, slashCommandList[index])
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].name < commands[j].name })
	return commands
}

var chatSlashTrie = newSlashTrie(slashCommandList)

func slashCommandSummary() string {
	values := make([]string, 0, len(slashCommandList))
	for _, command := range slashCommandList {
		values = append(values, command.usage)
	}
	return strings.Join(values, " ")
}

func slashCommandReference() string {
	var groups []string
	for _, group := range []string{"Session", "Display", "Governance", "Conversation"} {
		var commands []string
		for _, command := range slashCommandList {
			category := "Conversation"
			switch command.name {
			case "/new", "/resume", "/session", "/sessions", "/history":
				category = "Session"
			case "/tools":
				category = "Display"
			case "/govern", "/ungovern":
				category = "Governance"
			}
			if category == group {
				commands = append(commands, command.usage)
			}
		}
		groups = append(groups, group+":\n  "+strings.Join(commands, "\n  "))
	}
	return strings.Join(groups, "\n\n") + "\n\n/quit is an alias for /exit."
}

func slashMatches(line string) []slashCommand {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t") {
		return nil
	}
	return chatSlashTrie.find(line)
}

func slashHint(matches []slashCommand) string {
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match.usage)
	}
	return strings.Join(values, "  ")
}

func fitSlashHint(hint string, width int) string {
	limit := max(0, width-2)
	return ansi.Truncate(hint, limit, strings.Repeat(".", min(3, limit)))
}

func completeSlash(line string, matches []slashCommand) string {
	if len(matches) == 0 {
		return line
	}
	prefix := matches[0].name
	for _, match := range matches[1:] {
		for !strings.HasPrefix(match.name, prefix) {
			_, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
		}
	}
	if len(matches) == 1 && prefix == line && strings.Contains(matches[0].usage, " ") {
		return line + " "
	}
	if len(prefix) > len(line) {
		return prefix
	}
	return line
}

type lineScanner interface {
	Scan() bool
	Text() string
	Err() error
}

type chatInput struct {
	scanner  lineScanner
	in       *os.File
	out      *os.File
	renderer *chatRenderer
	enhanced bool
}

func newChatInput(scanner lineScanner, in *os.File, out io.Writer, renderer *chatRenderer) *chatInput {
	outFile, outputTerminal := out.(*os.File)
	enhanced := in != nil && outputTerminal && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(outFile.Fd())) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
	return &chatInput{scanner: scanner, in: in, out: outFile, renderer: renderer, enhanced: enhanced}
}

func (i *chatInput) readLine(ctx context.Context, hints bool) (string, error) {
	if !i.enhanced {
		return scanLine(ctx, i.scanner)
	}
	line, err := readSlashLine(ctx, i.in, i.out, i.renderer, hints)
	if err != errTerminalUnavailable {
		return line, err
	}
	i.enhanced = false
	return scanLine(ctx, i.scanner)
}

var errTerminalUnavailable = fmt.Errorf("terminal raw mode unavailable")
var errChatResized = fmt.Errorf("terminal resized; chat stopped without submitting the current input or approval; restart chat to continue")

// Explicit row breaks avoid terminal-dependent pending-wrap cursor positions.
func chatInputRows(prompt, line string, width int) []string {
	width = max(1, width)
	var text strings.Builder
	graphemes := uniseg.NewGraphemes(safeTerminal(line))
	for graphemes.Next() {
		cluster := graphemes.Str()
		if lipgloss.Width(cluster) > width {
			cluster = "?"
		}
		text.WriteString(cluster)
	}
	rows := strings.Split(ansi.Hardwrap(prompt+text.String(), width, true), "\n")
	if lipgloss.Width(rows[len(rows)-1]) == width {
		rows = append(rows, "")
	}
	return rows
}

func readSlashLine(ctx context.Context, in, out *os.File, renderer *chatRenderer, hints bool) (submittedLine string, readErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reader, err := cancelreader.NewReader(in)
	if err != nil {
		return "", errTerminalUnavailable
	}
	defer reader.Close()
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return "", errTerminalUnavailable
	}
	defer term.Restore(int(in.Fd()), state)
	width, height, _ := term.GetSize(int(out.Fd()))
	initialWidth, initialHeight := width, height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	resized := false
	checkResize := func() bool {
		w, h, err := term.GetSize(int(out.Fd()))
		resized = resized || (err == nil && (w != initialWidth || h != initialHeight))
		return resized
	}
	resizePoll := time.NewTicker(50 * time.Millisecond)
	defer resizePoll.Stop()
	// Only request one byte at a time: read-ahead would steal the next prompt's
	// input. Cancel and join the reader before restoring terminal settings.
	type result struct {
		value byte
		err   error
	}
	requests, stopped := make(chan struct{}), make(chan struct{})
	results := make(chan result, 1)
	go func() {
		defer close(stopped)
		for range requests {
			var one [1]byte
			_, err := io.ReadFull(reader, one[:])
			results <- result{one[0], err}
		}
	}()
	defer func() {
		reader.Cancel()
		close(requests)
		<-stopped
	}()
	pending := false
	nextByte := func(deadline time.Time) (byte, error) {
		if !pending {
			requests <- struct{}{}
			pending = true
		}
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			timeout = timer.C
		}
		for {
			if checkResize() {
				return 0, errChatResized
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-resizePoll.C:
				continue
			case <-timeout:
				return 0, context.DeadlineExceeded
			case value := <-results:
				pending = false
				return value.value, value.err
			}
		}
	}
	line := ""
	var utf8Bytes []byte
	renderer.mu.Lock()
	prompt := strings.Repeat(" ", renderer.promptIndent) + renderer.promptText
	renderer.mu.Unlock()
	initialRows := chatInputRows(prompt, "", width)
	cursorRow := len(initialRows) - 1
	if initialRows[cursorRow] == "" && cursorRow > 0 {
		cursorRow-- // The already printed prompt still has a pending wrap.
	}
	paintedRows := cursorRow + 1
	redraw := func(showHint, complete bool) {
		if checkResize() {
			return
		}
		var matches []slashCommand
		if hints && showHint {
			matches = slashMatches(line)
		}
		hint := fitSlashHint(slashHint(matches), width)
		renderer.mu.Lock()
		defer renderer.mu.Unlock()
		fmt.Fprint(out, "\r")
		if cursorRow > 0 {
			fmt.Fprintf(out, "\033[%dA", cursorRow)
		}
		for row := 0; row < paintedRows; row++ {
			if row > 0 {
				fmt.Fprint(out, "\r\n")
			}
			fmt.Fprint(out, "\033[2K")
		}
		if paintedRows > 1 {
			fmt.Fprintf(out, "\033[%dA", paintedRows-1)
		}
		rows := chatInputRows(prompt, line, width)
		if complete {
			// Submission is a durable transcript, not an editing viewport. Keep
			// every sanitized grapheme, even on terminals narrower than one glyph.
			rows = strings.Split(ansi.Hardwrap(prompt+safeTerminal(line), width, true), "\n")
			if lipgloss.Width(rows[len(rows)-1]) == width {
				rows = append(rows, "")
			}
		}
		// Keep editing bounded to the visible screen, even for a large paste.
		visible := max(1, height-1)
		if !complete && len(rows) > visible {
			marker := ansi.Truncate("[... input omitted ...]", width, strings.Repeat(".", min(3, width)))
			if visible > 2 {
				rows = append([]string{rows[0], marker}, rows[len(rows)-(visible-2):]...)
			} else if visible == 2 {
				rows = []string{marker, rows[len(rows)-1]}
			} else {
				rows = []string{marker}
			}
		}
		fmt.Fprint(out, "\r", strings.Join(rows, "\r\n"))
		cursorRow, paintedRows = len(rows)-1, len(rows)
		if hint != "" && height > len(rows) {
			fmt.Fprint(out, "\r\n\033[2K  ", safeTerminal(hint), "\033[1A\r")
			if column := lipgloss.Width(rows[len(rows)-1]); column > 0 {
				fmt.Fprintf(out, "\033[%dC", column)
			}
			paintedRows++
		}
	}
	redraw(true, false)
	defer func() {
		if readErr == nil && ctx.Err() != nil {
			submittedLine, readErr = "", ctx.Err()
		}
		redraw(false, readErr == nil)
		if resized {
			// Reflow differs by terminal. Never erase or move upward using stale
			// coordinates; abandon input and leave existing screen contents intact.
			submittedLine, readErr = "", errChatResized
			fmt.Fprint(out, "\r\n")
		}
		fmt.Fprint(out, "\r\n")
		renderer.submitted(true)
	}()
	escapeState := 0
	var escapeDeadline time.Time
	for {
		if err := ctx.Err(); err != nil {
			return "", ctx.Err()
		}
		key, err := nextByte(escapeDeadline)
		if checkResize() {
			return "", errChatResized
		}
		if err == context.DeadlineExceeded && ctx.Err() == nil {
			escapeState, escapeDeadline = 0, time.Time{}
			continue
		}
		if err != nil {
			return "", err
		}
		if key == 3 {
			return "", context.Canceled
		}
		if key == 0x1b {
			escapeState, escapeDeadline = 1, time.Now().Add(100*time.Millisecond)
			utf8Bytes = nil
			continue
		}
		if escapeState != 0 {
			previous := escapeState
			escapeState, escapeDeadline = 0, time.Time{}
			if previous == 1 && (key == '[' || key == 'O') {
				escapeState, escapeDeadline = 2, time.Now().Add(100*time.Millisecond)
				continue
			}
			if previous == 2 && key >= 0x20 && key <= 0x7e {
				if key < 0x40 {
					escapeState, escapeDeadline = 2, time.Now().Add(100*time.Millisecond)
				}
				continue
			}
		}
		if key < 0x20 || key == 127 {
			utf8Bytes = nil
		}
		switch key {
		case '\r', '\n':
			return line, nil
		case 4:
			if line == "" {
				return "", io.EOF
			}
		case 8, 127:
			if line != "" {
				graphemes := uniseg.NewGraphemes(line)
				last := 0
				for graphemes.Next() {
					last, _ = graphemes.Positions()
				}
				line = line[:last]
				redraw(true, false)
			}
		case '\t':
			if hints {
				line = completeSlash(line, slashMatches(line))
			}
			redraw(true, false)
		default:
			if key >= 0x20 && key != 0x7f && len(line) < 64<<10 {
				utf8Bytes = append(utf8Bytes, key)
				for len(utf8Bytes) > 0 && utf8.FullRune(utf8Bytes) {
					r, size := utf8.DecodeRune(utf8Bytes)
					if r != utf8.RuneError || size > 1 {
						line += safeTerminal(string(r))
					}
					utf8Bytes = utf8Bytes[size:]
					redraw(true, false)
				}
			}
		}
	}
}
