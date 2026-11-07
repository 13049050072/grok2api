package console

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

type consoleFeedbackRecordingRepository struct {
	consoleEgressRepositoryStub
	lookups atomic.Int32
}

func (r *consoleFeedbackRecordingRepository) GetEgressNode(context.Context, uint64) (egressdomain.Node, error) {
	r.lookups.Add(1)
	return egressdomain.Node{}, errors.New("not found")
}

func TestReportRequestFailureSkipsCancellationsAndBusinessErrors(t *testing.T) {
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		err      error
		reported bool
	}{
		{name: "transport", ctx: context.Background(), err: errors.New("dial tcp: connection refused"), reported: true},
		{name: "request timeout", ctx: context.Background(), err: context.DeadlineExceeded, reported: true},
		{name: "client canceled", ctx: canceled, err: errors.New("read: use of closed network connection")},
		{name: "client canceled without wrapping", ctx: canceled, err: context.DeadlineExceeded},
		{name: "downstream closed", ctx: context.Background(), err: io.ErrClosedPipe},
		{name: "business", ctx: context.Background(), err: provider.NewUpstreamBusinessError(errors.New("upstream rejected"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &consoleFeedbackRecordingRepository{}
			adapter := NewAdapter(Config{}, infraegress.NewManager(repository, cipher), cipher, nil)
			adapter.reportRequestFailure(test.ctx, 7, test.err)
			if reported := repository.lookups.Load() > 0; reported != test.reported {
				t.Fatalf("egress feedback reported = %v, want %v", reported, test.reported)
			}
		})
	}
}

func TestConsoleRetryAfterWithRunesThatChangeLengthWhenLowered(t *testing.T) {
	body := []byte(strings.Repeat("Ⱥ", 16) + " Resets in: 2m5s")
	if got := consoleRetryAfter(body); got != 2*time.Minute+5*time.Second {
		t.Fatalf("consoleRetryAfter() = %v, want 2m5s", got)
	}
}
