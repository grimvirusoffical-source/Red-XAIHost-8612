// Package sec implements the `.red-xai-sec` format: access rules plus
// at-rest encryption helpers.
//
// A .red-xai-sec file is a Box document that declares roles, scopes, rules
// and encryption requirements (see Parse and docs/phase3b-4.md). Policy.Authorize
// answers "may this principal do this action to this resource?" and denies by
// default.
//
// The encryption half (Encrypt, Decrypt, Rewrap) uses AES-256-GCM from the
// standard library. Every encryption creates a fresh random per-file data
// key and wraps it with a master key that the caller supplies. This package
// never touches the file system, the network or the log: master keys exist
// only in memory, and no formatting verb prints them.
//
// Nothing here is wired to the real storage engines or the host yet.
package sec
