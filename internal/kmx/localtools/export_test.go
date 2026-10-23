package localtools

// These bridges exist only in the test package build, never in the CLI/library.
var NewMCPHostForTest = newMCPHost
var MCPFixtureForTest = newMCPFixture
