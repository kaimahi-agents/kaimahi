package admin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

type reportTerminal struct {
	bytes.Buffer
	fd int
}

func (w *reportTerminal) Fd() uintptr { return uintptr(w.fd) }

func TestAdminReportsRichPlainAndNumericParity(t *testing.T) {
	identifier := "model-with-an-identifier-longer-than-twenty-eight-bytes"
	delivery := "delivery-identifier-longer-than-twenty-four-bytes"
	for _, mode := range []string{"plain", "rich", "no-color"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			if mode == "no-color" {
				t.Setenv("NO_COLOR", "1")
			}
			var plain bytes.Buffer
			var out io.Writer = &plain
			text := plain.String
			if mode != "plain" {
				fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { unix.Close(fd) })
				if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: 42, Row: 24}); err != nil {
					t.Fatal(err)
				}
				terminal := &reportTerminal{fd: fd}
				out, text = terminal, terminal.String
			}
			c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/admin/ledger":
					fmt.Fprintf(w, `{"entries":[{"model":%q,"status":403,"cost_source":"denied","cost_cents":0,"input_tokens":0,"output_tokens":0},{"model":"successful","status":200,"cost_source":"priced","cost_cents":1234567},{"model":"upstream-error","status":403,"cost_source":"free","cost_cents":0}],"month_cents":1234567,"month_tokens":9007199254740993}`, identifier)
				case "/admin/inbound-audit":
					fmt.Fprintf(w, `{"entries":[{"hook":"hook","delivery_id":%q,"decision":"admitted"}]}`, delivery)
				default:
					io.WriteString(w, `{"entries":[]}`)
				}
			}))
			if err := c.Flow(out, ""); err != nil {
				t.Fatal(err)
			}
			got := ansi.Strip(text())
			if !strings.Contains(got, "4 events, 1234567 cents, 1 refused") || !strings.Contains(got, "403") || !strings.Contains(got, "200") {
				t.Fatalf("numeric/state mismatch: %s", got)
			}
			if mode == "plain" {
				if strings.Contains(got, identifier) || strings.Contains(got, delivery) {
					t.Fatalf("changed redirected truncation: %s", got)
				}
			} else {
				compact := strings.Join(strings.Fields(got), "")
				for _, want := range []string{identifier, delivery, "Flow(4)", "denied"} {
					if !strings.Contains(compact, want) {
						t.Errorf("rich report lost %q: %s", want, got)
					}
				}
			}
			if err := c.Ledger(out, ""); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text(), "9007199254740993") {
				t.Fatal("large token count rounded")
			}
			if err := c.ToolAudit(out, ""); err != nil {
				t.Fatal(err)
			}
			if mode != "plain" && !strings.Contains(ansi.Strip(text()), "Tool audit (0)") {
				t.Fatal("empty report lost title/count")
			}
			if mode != "rich" && strings.Contains(text(), "\x1b") {
				t.Fatal("unexpected ANSI")
			}
		})
	}
}
