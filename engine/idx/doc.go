// Package idx implements RedX phase 3, part 1: the .red-xai-db-idx search
// format.
//
// An index declaration is a Box (see ParseDecls) that names a file, the
// fields to index and the kind of index:
//
//	{PlayerByName[]} = [
//	    {File[]}   = "players.red-xai.db",
//	    {Box[]}    = "Player",
//	    {Fields[]} = ;["Name"]:,
//	    {Kind[]}   = "exact"
//	];
//
// Kinds are exact, prefix, range (numbers) and text (word tokens with AND/OR
// queries). An Index is kept up to date through the Observer interface
// (ObservedStore wraps any Store and calls the observers after every
// successful Put/Delete), can be rebuilt from a scan of the source, and can be
// saved to a sealed snapshot through a Blob. A snapshot that is missing,
// truncated, corrupt, from another declaration, or stale compared with the
// source is detected on Load and the index is rebuilt.
//
// The package talks to storage only through the small Scanner/Store
// interfaces (link.Store satisfies Store) and is tested against
// link.MemStore; it is not wired to the real storage engines yet.
package idx
