// Package fileaccess provides package-independent checked filesystem access
// through opaque handles.
//
// It opens existing files and directories without following links, validates
// private or public-ancestor security, reports physical object identity, and
// provides private first creation, process-held locking, and durable no-replace
// publication. Lock callers can distinguish observed contention without changing
// the legacy error category. Checked private regular files can be flushed; Unix
// reclamation removes only identity-matched empty private children and flushes
// their parent, never recursively deleting a tree. A removal error can follow
// deletion and does not authorize retry. Windows directory reclamation remains
// unsupported. A failed publication reports only positively observed visibility;
// a false visibility bit with an error never proves absence. On Windows, every
// retained handle is qualified as local-disk NTFS with persistent ACLs. On Linux,
// writable-state callers can refuse recognized WSL Windows-drive mounts before
// creation. It interprets no application state, repairs or adopts no existing
// object, retries no ambiguous effect, exposes no raw descriptor, and is consumed
// only by explicit state, local-preview, and retained-diagnostic adapters.
package fileaccess
