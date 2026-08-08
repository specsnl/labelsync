package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

// What main does with the error it gets back: print it once through the writer,
// or print nothing at all when the non-zero code is an outcome.
func TestReport(t *testing.T) {
	inaccessible := fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible)

	for _, tc := range []struct {
		name     string
		err      error
		wantKind string // "" means nothing may be written
		wantCode exit.Code
	}{
		{
			name:     "success prints nothing",
			err:      nil,
			wantCode: exit.OK,
		},
		{
			name:     "a failure is printed with its kind",
			err:      inaccessible,
			wantKind: "repo_inaccessible",
			wantCode: exit.Error,
		},
		{
			name:     "a carried failure keeps its kind",
			err:      &exit.Err{Code: exit.Skipped, Err: inaccessible},
			wantKind: "repo_inaccessible",
			wantCode: exit.Skipped,
		},
		{
			name:     "a silent carrier prints nothing",
			err:      &exit.Err{Code: exit.Drift},
			wantCode: exit.Drift,
		},
		{
			name:     "drift and skipped combine",
			err:      &exit.Err{Code: exit.Drift | exit.Skipped},
			wantCode: 6,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			report(output.NewJSONWriter(&stdout, &stderr), tc.err)

			if stdout.Len() != 0 {
				t.Errorf("a failure reached stdout: %q", stdout.String())
			}

			switch tc.wantKind {
			case "":
				if stderr.Len() != 0 {
					t.Errorf("want nothing printed, got: %q", stderr.String())
				}
			default:
				if !strings.Contains(stderr.String(), `"error_kind":"`+tc.wantKind+`"`) {
					t.Errorf("stderr = %q, want error_kind %q", stderr.String(), tc.wantKind)
				}
			}

			if code := exit.Of(tc.err); code != tc.wantCode {
				t.Errorf("exit code = %s, want %s", code, tc.wantCode)
			}
		})
	}
}

// The silent path is about the Err field, not the code: a carrier holding a real
// failure has to print even though it also carries an outcome-shaped code.
func TestReport_SilenceFollowsTheErrFieldNotTheCode(t *testing.T) {
	var stdout, stderr bytes.Buffer

	report(output.NewPrettyWriter(&stdout, &stderr, []string{}), &exit.Err{
		Code: exit.Skipped,
		Err:  errors.New("it broke"),
	})

	if !strings.Contains(stderr.String(), "it broke") {
		t.Errorf("stderr = %q, want the wrapped failure", stderr.String())
	}
}
