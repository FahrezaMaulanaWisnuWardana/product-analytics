package analytics

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapDatabaseError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want error
	}{
		{"collection limit", "54011", ErrCollaborationLimit},
		{"workspace limit", "P0001", ErrWorkspaceLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &pgconn.PgError{Code: tc.code, Message: "saved_reports has reached the per-project limit of 1000 rows"}
			mapped := mapDatabaseError(raw)
			if !errors.Is(mapped, tc.want) {
				t.Fatalf("got %v, want %v", mapped, tc.want)
			}
			if errors.Is(mapped, ErrProjectNotFound) || len(mapped.Error()) > len(tc.want.Error())+1 {
				t.Fatalf("mapped error leaks driver detail: %v", mapped)
			}
		})
	}

	raw := &pgconn.PgError{Code: "23505", Message: "duplicate key detail"}
	if mapped := mapDatabaseError(raw); !errors.Is(mapped, raw) {
		t.Fatalf("unrelated database error was replaced: %v", mapped)
	}
	plain := errors.New("invalid report")
	if mapped := mapDatabaseError(plain); !errors.Is(mapped, plain) {
		t.Fatalf("validation error was replaced: %v", mapped)
	}
}

func TestIsDatabaseError(t *testing.T) {
	if !IsDatabaseError(&pgconn.PgError{Code: "54011"}) || !IsDatabaseError(ErrCollaborationLimit) {
		t.Fatal("database errors not recognized")
	}
	if IsDatabaseError(errors.New("invalid report")) {
		t.Fatal("validation error reported as database error")
	}
}
