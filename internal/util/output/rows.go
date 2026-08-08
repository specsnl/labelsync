package output

// Column describes one column of a pretty table: the heading a human reads, and
// how a row renders in that column.
//
// The cell function is what lets the two audiences disagree without either one
// compromising. A size is an int64 in the JSON record and "1.2 MiB" in the
// table; a timestamp is RFC 3339 in the record and "3 days ago" in the table. A
// column with no backing field at all — a computed total, a marker — is just a
// function that ignores most of its argument.
type Column[T any] struct {
	// Header is the column heading. Prose: it may be reworded freely, because no
	// machine reads it.
	Header string

	// Cell renders one row in this column.
	Cell func(T) string
}

// Col builds a [Column]. It exists for type inference: T is deduced from the
// cell function, so a call site names the row type once rather than once per
// column.
//
//	output.Table(w, groups,
//	    output.Col("Group", func(g GroupRow) string { return g.Name }),
//	    output.Col("Repositories", func(g GroupRow) string { return strconv.Itoa(g.Count) }),
//	)
func Col[T any](header string, cell func(T) string) Column[T] {
	return Column[T]{Header: header, Cell: cell}
}

// TableData is a table prepared for both audiences: display strings for the
// human, and the source values themselves for the machine.
//
// Build it with [Table] rather than by hand. Going through the constructor is
// what guarantees every row has exactly one cell per header — the alignment a
// [][]string could silently get wrong.
type TableData struct {
	// Headers are the column headings, in order.
	Headers []string

	// Cells are the rendered rows, each with one entry per header.
	Cells [][]string

	// Records are the source rows, one per entry in Cells, marshalled as-is by
	// [JSONWriter.WriteTable].
	Records []any
}

// Table writes rows as the command's product: a bordered table for a human, one
// JSON object per row for a machine.
//
// The two renderings come from different places, which is the point. The table
// comes from cols. The JSON comes from marshalling each row directly, so its
// keys and types are the struct's own:
//
//	type GroupRow struct {
//	    Name         string `json:"group"`
//	    Repositories int    `json:"repositories"`
//	}
//
// gives {"group":"websites","repositories":12} — a number, not "12", so a
// consumer can filter on it. Those json tags are a public contract in the same
// way error_kind is: they may be added to, not renamed.
//
// Passing something other than a tagged struct is legal and rarely what you
// want: a plain string row marshals to a bare JSON string, which is a valid
// NDJSON line that no field selector can read.
func Table[T any](w Writer, rows []T, cols ...Column[T]) {
	data := TableData{
		Headers: make([]string, len(cols)),
		Cells:   make([][]string, len(rows)),
		Records: make([]any, len(rows)),
	}

	for i, col := range cols {
		data.Headers[i] = col.Header
	}

	for i, row := range rows {
		cells := make([]string, len(cols))
		for j, col := range cols {
			cells[j] = col.Cell(row)
		}

		data.Cells[i] = cells
		data.Records[i] = row
	}

	w.WriteTable(data)
}
