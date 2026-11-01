package agentfile

// LoadCopilotdPair shares bounded checked pair intake, retaining exact native
// tool names rather than applying embedded tool-set alias exclusions. All
// fixtures and tool assertions still require an explicit declared tool.
func LoadCopilotdPair(agentFile, testsOverride string) (*Pair, error) {
	return loadPair(agentFile, testsOverride, func(filename string, data []byte) (*Agent, error) {
		return parseWithTools(filename, data, true)
	})
}
