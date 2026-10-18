// Package query implements the RedX SQL-like query language over Box data.
//
//	SELECT Name, Stats.Level FROM players.Player WHERE Level >= ? ORDER BY Name LIMIT 10
//	SELECT p.Name, g.Title FROM players.Player p JOIN p.Guild g WHERE g.Title LIKE 'Red%'
//	INSERT INTO players.Player (_id, Name, Level) VALUES (7, 'ann', 1)
//	UPDATE players.Player SET Level = Level + 1 WHERE _id = 7
//	DELETE FROM players.Player WHERE Level < 1
//	EXPLAIN SELECT ... (shows the plan, runs nothing)
//
// A file holds records (top-level entries such as Player[ID=1]); a record is
// a box of fields. The model is schema-less: a field that does not exist
// reads as NELL. The language, its limits and its authorization rules are
// described in docs/phase5.md.
//
// The package talks to storage through link.Store, uses idx indexes when a
// matching index exists, follows @> links (link.Engine) for JOIN and for
// cascade deletes, and asks a sec Authorizer before touching every resource
// (default deny). It depends on nothing outside the standard library and the
// other redx packages. It is tested against link.MemStore and, through the host,
// against the real engines (POST /v1/sql, see docs/HOSTING.md).
package query
