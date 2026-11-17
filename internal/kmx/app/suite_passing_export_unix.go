//go:build !windows

package app

// Unix retains the established os.Root publisher while the managed state store
// is migrated. Windows has no legacy receipts and uses the checked foundation.
const checkedPrivateSuiteExport = false
