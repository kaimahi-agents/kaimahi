package modelaccess

// Install is guidance only. Sizes are download estimates, not installed disk/RAM.
// Unknown model/runtime sizes stay explicit rather than triggering a download.
type Install struct {
	URL          string `json:"url"`
	Requirements string `json:"requirements"`
	RuntimeSize  string `json:"runtimeSize"`
	ModelSize    string `json:"modelSize"`
	Recommended  bool   `json:"recommended"`
}

func InstallGuidance(provider, goos, arch string) Install {
	g := Install{RuntimeSize: "unknown; see download page", ModelSize: "unknown; depends on the selected model"}
	if provider == "foundry-local" {
		g.URL = "https://github.com/microsoft/Foundry-Local/releases/tag/cli-preview-0.11.0"
		g.Requirements = "Foundry Local CLI preview 0.11; supported OS/architecture and model-specific memory required"
		switch goos + "/" + arch {
		case "windows/amd64":
			g.RuntimeSize = "about 24.2 MB (CLI 0.11.0, 2026-10-07)"
			g.Recommended = true
		case "windows/arm64":
			g.RuntimeSize = "about 23.7 MB (CLI 0.11.0, 2026-10-07)"
			g.Recommended = true
		case "darwin/arm64":
			g.RuntimeSize = "about 48.4 MB (CLI 0.11.0, 2026-10-07)"
		case "linux/amd64":
			g.RuntimeSize = "about 57.6 MB (CLI 0.11.0, 2026-10-07)"
		case "linux/arm64":
			g.RuntimeSize = "about 54.8 MB (CLI 0.11.0, 2026-10-07)"
		default:
			g.Requirements = "not supported on this OS/architecture; Foundry Local supports Windows x64/ARM64, Apple Silicon macOS, Linux x64/ARM64"
		}
	} else {
		g.URL = "https://ollama.com/download"
		switch goos {
		case "windows":
			g.Requirements = "Windows 10 22H2 or newer; at least 4 GB installed binary space plus model storage; optional supported GPU drivers"
		case "darwin":
			g.Requirements = "macOS 14 Sonoma or newer; Apple M series or x86 CPU; additional model storage"
		case "linux":
			g.Requirements = "Linux x64/ARM64; additional model storage; optional supported GPU drivers"
		default:
			g.Requirements = "check the official platform requirements"
		}
	}
	return g
}
