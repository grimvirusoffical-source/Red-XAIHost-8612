// Package link implements RedX phase 2, part 1: the .red-xai-db-link format.
//
// A link is a Box value (see package box): `"file":Box[ID=1].Key @> "other":Box[ID=2]`.
//
//	@>   read link: the target can be read through the source (Resolve, Expand).
//	@!>  cascade link: deleting the source record also deletes the target record.
//
// Links come from two places:
//
//  1. Embedded links: a field of a record whose value is a link (anywhere in
//     nested boxes or arrays). The record that holds the field is the owner.
//  2. Standalone links: entries of a .red-xai-db-link file (see LinkSet).
//     The record named by the link's From reference is the owner.
//
// The Engine works against the small Store interface so it can be tested with
// MemStore today and wired to the real storage engines later.
package link
