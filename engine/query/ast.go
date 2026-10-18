package query

import (
	"redx/box"
)

// Stmt is a parsed statement: *Select, *Insert, *Update, *Delete or *Explain.
type Stmt interface{ stmtPos() Pos }

// Expr is a parsed expression.
type Expr interface{ exprPos() Pos }

// Source names the records a statement reads or writes: FROM file[.Box] [alias].
type Source struct {
	P     Pos
	File  string // as written; resolved against the store at run time
	Box   string // "" = every record of the file
	Alias string // explicit alias, or "" (the default alias is then Box, else File)
}

// SelectItem is one entry of the select list.
type SelectItem struct {
	Star      bool   // * or alias.*
	StarAlias string // for alias.*
	Expr      Expr
	As        string
	Text      string // source text (column name when As is empty)
	P         Pos
}

// Join is JOIN alias.field [AS] alias2, following @> links stored in field.
type Join struct {
	P     Pos
	Left  bool
	Via   *Field
	Alias string
}

// OrderItem is one ORDER BY key.
type OrderItem struct {
	Expr Expr
	Desc bool
}

// Select is a SELECT statement.
type Select struct {
	P      Pos
	Items  []SelectItem
	From   Source
	Joins  []Join
	Where  Expr
	Order  []OrderItem
	Limit  Expr // *Lit or *Param, nil if absent
	Offset Expr
}

// Insert is INSERT INTO file.Box (cols) VALUES (...), (...).
type Insert struct {
	P      Pos
	Target Source
	Cols   []*Field
	Rows   [][]Expr
}

// Assign is one `field = expr` of an UPDATE.
type Assign struct {
	Target *Field
	Value  Expr
}

// Update is UPDATE file.Box SET ... WHERE ....
type Update struct {
	P      Pos
	Target Source
	Sets   []Assign
	Where  Expr
}

// Delete is DELETE FROM file.Box WHERE ....
type Delete struct {
	P      Pos
	Target Source
	Where  Expr
}

// Explain wraps a statement; it plans but does not run it.
type Explain struct {
	P     Pos
	Inner Stmt
}

func (s *Select) stmtPos() Pos  { return s.P }
func (s *Insert) stmtPos() Pos  { return s.P }
func (s *Update) stmtPos() Pos  { return s.P }
func (s *Delete) stmtPos() Pos  { return s.P }
func (s *Explain) stmtPos() Pos { return s.P }

// Expression nodes.
type (
	// Lit is a literal value.
	Lit struct {
		P Pos
		V box.Value
	}
	// Param is a ? placeholder; N is its 0-based position in the query.
	Param struct {
		P Pos
		N int
	}
	// Field is a field path such as Name, Stats.Level, p.Name or Inv[ID=3].Count.
	// Alias/Path/Pseudo are filled in by binding.
	Field struct {
		P    Pos
		Segs []box.Seg // as written
		Raw  string

		Alias  int       // index of the source alias the field reads from (0 = FROM)
		Path   []box.Seg // path inside that record
		Pseudo string    // "_id", "_box" or "_file" for the pseudo fields
		// Names are the dotted field names of Path without ID selectors
		// ("Stats", "Stats.Level"), cumulative, used for authorization.
		Names []string
	}
	// Unary is -x.
	Unary struct {
		P  Pos
		Op string
		X  Expr
	}
	// Not is NOT x.
	Not struct {
		P Pos
		X Expr
	}
	// Binary is x op y: and, or, =, !=, <, <=, >, >=, +, -, *, /.
	Binary struct {
		P    Pos
		Op   string
		L, R Expr
	}
	// In is x [NOT] IN (a, b, ...).
	In struct {
		P    Pos
		X    Expr
		List []Expr
		Neg  bool
	}
	// Like is x [NOT] LIKE 'prefix%'.
	Like struct {
		P   Pos
		X   Expr
		Pat Expr
		Neg bool
	}
	// Between is x [NOT] BETWEEN lo AND hi (inclusive).
	Between struct {
		P      Pos
		X      Expr
		Lo, Hi Expr
		Neg    bool
	}
	// IsNell is x IS [NOT] NELL.
	IsNell struct {
		P   Pos
		X   Expr
		Neg bool
	}
	// Match is MATCH(field, 'words') (all words) or MATCH_ANY(field, 'words').
	Match struct {
		P     Pos
		Field *Field
		Terms Expr
		Any   bool
	}
)

func (e *Lit) exprPos() Pos     { return e.P }
func (e *Param) exprPos() Pos   { return e.P }
func (e *Field) exprPos() Pos   { return e.P }
func (e *Unary) exprPos() Pos   { return e.P }
func (e *Not) exprPos() Pos     { return e.P }
func (e *Binary) exprPos() Pos  { return e.P }
func (e *In) exprPos() Pos      { return e.P }
func (e *Like) exprPos() Pos    { return e.P }
func (e *Between) exprPos() Pos { return e.P }
func (e *IsNell) exprPos() Pos  { return e.P }
func (e *Match) exprPos() Pos   { return e.P }
