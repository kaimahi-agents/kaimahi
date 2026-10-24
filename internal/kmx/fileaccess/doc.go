// Package fileaccess provides package-independent checked filesystem access
// through opaque handles.
//
// It opens existing files and directories without following links, validates
// private or public-ancestor security, reports physical object identity, and
// provides private first creation, process-held locking, and durable no-replace
// publication. On Windows, every retained handle is qualified as local-disk
// NTFS with persistent ACLs. It interprets no application state, repairs or
// adopts no existing object, retries no ambiguous effect, exposes no raw
// descriptor, and remains unwired from operation adapters.
package fileaccess
