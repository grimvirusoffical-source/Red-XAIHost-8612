package query

// Limits bounds what one query may cost. Zero fields take the defaults
// (see DefaultLimits); to run without a bound set a very large value
// explicitly.
type Limits struct {
	MaxQueryBytes  int // query text length (default 64 KiB)
	MaxTokens      int // tokens in one query (8192)
	MaxDepth       int // expression nesting (64)
	MaxInList      int // items in one IN (...) (1000)
	MaxJoins       int // JOINs in one SELECT (4)
	MaxInsertRows  int // rows in one INSERT (1000)
	MaxPathDepth   int // segments in one field path (16)
	MaxParamBytes  int // size of one string ? argument (1 MiB)
	MaxRows        int // rows one SELECT may return (10000)
	MaxScan        int // records read from storage by one statement (1,000,000)
	MaxWrites      int // records one INSERT/UPDATE/DELETE may change, cascades included (10000)
	MaxResultBytes int // approximate size of a SELECT result (16 MiB)
	MaxColumns     int // distinct columns in one result (1024)
}

// DefaultLimits returns the limits used when none are configured.
func DefaultLimits() Limits { return Limits{}.withDefaults() }

func (l Limits) withDefaults() Limits {
	def := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&l.MaxQueryBytes, 64<<10)
	def(&l.MaxTokens, 8192)
	def(&l.MaxDepth, 64)
	def(&l.MaxInList, 1000)
	def(&l.MaxJoins, 4)
	def(&l.MaxInsertRows, 1000)
	def(&l.MaxPathDepth, 16)
	def(&l.MaxParamBytes, 1<<20)
	def(&l.MaxRows, 10000)
	def(&l.MaxScan, 1000000)
	def(&l.MaxWrites, 10000)
	def(&l.MaxResultBytes, 16<<20)
	def(&l.MaxColumns, 1024)
	return l
}
