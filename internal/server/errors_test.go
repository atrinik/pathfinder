package server

import (
	"context"
	"errors"
	"testing"

	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/search"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSearchErrorsMapToStatusCodes(t *testing.T) {
	for err, want := range map[error]codes.Code{
		search.ErrBusy:           codes.ResourceExhausted,
		atlas.ErrBusy:            codes.ResourceExhausted,
		atlas.ErrInvalid:         codes.InvalidArgument,
		atlas.ErrCorrupt:         codes.DataLoss,
		context.Canceled:         codes.Canceled,
		context.DeadlineExceeded: codes.DeadlineExceeded,
		search.ErrInvalid:        codes.InvalidArgument,
		errors.New("other"):      codes.InvalidArgument,
	} {
		if got := status.Code(searchError(err)); got != want {
			t.Errorf("%v: %v, want %v", err, got, want)
		}
	}
}
