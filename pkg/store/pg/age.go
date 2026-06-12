// Package pg implements the PostgreSQL/Apache AGE backed SGP store.
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// execCypher executes a Cypher query against the 'sgp' AGE graph.
// The cypher string is embedded directly as a dollar-quoted literal, which
// is what AGE requires. Callers must not include $$ in the cypher string.
// Since all Cypher calls in this package are hardcoded (not user-supplied),
// string interpolation via [fmt.Sprintf] is safe for the parameter values.
//
// search_path = ag_catalog must already be set on the connection; the pgxpool
// AfterConnect hook and the migration both ensure this.
func execCypher(ctx context.Context, tx pgx.Tx, cypher string) error {
	sql := `SELECT * FROM cypher('sgp', $$ ` + cypher + ` $$) AS (result agtype)` //nolint:unqueryvet // AGE uses agtype

	_, err := tx.Exec(ctx, sql)
	if err != nil {
		return fmt.Errorf("cypher %q: %w", cypher, err)
	}

	return nil
}

// stripAgtypeQuotes removes the surrounding double-quotes that AGE adds to
// agtype string values when cast to ::text (e.g. `"some-uuid"` → `some-uuid`).
func stripAgtypeQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}

	return s
}

// escapeSingleQuotes escapes single quotes for safe embedding in Cypher strings.
func escapeSingleQuotes(s string) string {
	const escapeBufExtra = 4 // reserve space for a few likely escapes

	out := make([]byte, 0, len(s)+escapeBufExtra)
	for i := range len(s) {
		if s[i] == '\'' {
			out = append(out, '\\', '\'')
		} else {
			out = append(out, s[i])
		}
	}

	return string(out)
}
