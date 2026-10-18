// Package pack implements RedX phase 3, part 2: the .red-xai-pack format for
// files and folders.
//
// File contents are cut into fixed-size chunks. Each chunk is stored once under
// the lower-case hex SHA-256 of its bytes (a content-addressed chunk store, so
// identical chunks, within or across files, are shared). Every folder has a
// manifest, written in the Box language, that lists its files (name, size,
// SHA-256 of the whole file, chunk size, chunk ids) and its sub-folders:
//
//	{Manifest[]} = [
//	    {Version[]} = 1,
//	    {Folder[]}  = "docs",
//	    {File[ID=1]} = [ {Name[]} = "a.txt", {Size[]} = 12, {SHA256[]} = "...",
//	                     {ChunkSize[]} = 1048576, {Chunks[]} = ;["..."]: ],
//	    {Dir[ID=2]}  = [ {Name[]} = "images" ]
//	];
//	{Seal[]} = "<sha256 of every byte above>";
//
// A Pack works against two small interfaces (ChunkStore and ManifestStore) so
// it can be tested with an in-memory fake (MemBackend) and used on a
// directory (DirBackend). Reads verify every chunk (size and hash) and the
// whole file (size and hash) and report truncated, corrupt or missing chunks.
// All names go through CleanPath, which rejects path traversal and other
// unsafe names.
//
// Not wired to the real storage engines or the host yet.
package pack
