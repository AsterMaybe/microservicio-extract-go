package presentation_test

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"microservicio-go/application"
	"microservicio-go/domain"
)

// holdProcessor ignores its context entirely and pins a worker slot until
// release is closed. Modelling a held slot this way matters: a processor that
// respects its context would drop the slot as soon as that request's own
// admission budget expired, which is not what "a slot is busy" looks like.
type holdProcessor struct{ release chan struct{} }

func (h *holdProcessor) ExtractText(context.Context, []byte) (string, int, error) {
	<-h.release
	return "text", 1, nil
}

// hangProcessor blocks but still honours cancellation.
type hangProcessor struct{ release chan struct{} }

func (h *hangProcessor) ExtractText(ctx context.Context, _ []byte) (string, int, error) {
	select {
	case <-h.release:
		return "text", 1, nil
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}
}

type noopRepo struct{}

func (noopRepo) Save(context.Context, *domain.ExtractionRecord) error { return nil }

// TestExtract_FullQueueShedsImmediately is FILTER 1. With QUEUE_SIZE places
// taken and every parse slot busy, the next arrival must be refused with 429
// without waiting and without reading the body.
//
// This is the regression test for the layer bug that made QUEUE_SIZE dead
// config: the bounded queue used to live inside the use case, downstream of
// MAX_IN_FLIGHT, so it could never fill (waiting was capped at
// MAX_IN_FLIGHT-CONCURRENCY) and this branch was unreachable.
func TestExtract_FullQueueShedsImmediately(t *testing.T) {
	const queueSize = 3

	proc := &holdProcessor{release: make(chan struct{})}

	uc := application.NewExtractTextUseCase(proc, noopRepo{}, nil, application.Limits{Workers: 1})

	cfg := testConfig()
	cfg.QueueSize = queueSize
	cfg.MaxInFlight = 32
	cfg.AdmissionTimeout = 30 * time.Second // never expire: only the queue can shed
	cfg.ExtractionTimeout = 30 * time.Second
	router := newTestRouter(t, uc, &stubPinger{}, cfg)

	// Fill every queue place. They park before reading their bodies.
	var wg sync.WaitGroup
	for i := 0; i < queueSize; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := uploadRequest(t, "/extract", "held.pdf",
				[]byte("%PDF-1.7\nheld "+strconv.Itoa(i)))
			perform(t, router, req)
		}(i)
	}
	time.Sleep(250 * time.Millisecond) // let them all take a queue place

	start := time.Now()
	req := uploadRequest(t, "/extract", "overflow.pdf", []byte("%PDF-1.7\none too many"))
	rec := perform(t, router, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the bounded queue is full (queue=%d)", rec.Code, queueSize)
	}
	if elapsed > 2*time.Second {
		t.Errorf("shed took %s; a full queue must reject immediately, not wait", elapsed)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("429 must carry Retry-After so clients back off instead of hot-looping")
	}
	body := decodeProblem(t, rec)
	if body["type"] == nil || body["status"] == nil {
		t.Errorf("expected a problem+json body, got %v", body)
	}

	// Release the held slot, then let the parked requests drain.
	close(proc.release)
	wg.Wait()
}

// TestExtract_AdmissionTimeoutShedsWith429 is FILTER 2. With queue space
// available but no parse slot freeing up, the request must give up when its
// admission budget expires and report 429 — not 504. Waiting behind other work
// says nothing about how slow the caller's document is, and a 504 would tell
// well-behaved clients not to retry during a load event.
func TestExtract_AdmissionTimeoutShedsWith429(t *testing.T) {
	const admission = 150 * time.Millisecond

	proc := &holdProcessor{release: make(chan struct{})}
	defer close(proc.release)

	// ADMISSION_TIMEOUT is one budget enforced at both waits: here for RAM to
	// buffer the body, in the use case for a parse slot.
	uc := application.NewExtractTextUseCase(proc, noopRepo{}, nil, application.Limits{
		Workers:   1,
		Admission: admission,
	})

	cfg := testConfig()
	cfg.QueueSize = 50 // room in the queue
	cfg.MaxInFlight = 32
	cfg.AdmissionTimeout = admission
	cfg.ExtractionTimeout = 30 * time.Second // must NOT be what fires
	router := newTestRouter(t, uc, &stubPinger{}, cfg)

	// Occupy the only worker slot.
	go perform(t, router, uploadRequest(t, "/extract", "hog.pdf", []byte("%PDF-1.7\nhog")))
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	rec := perform(t, router, uploadRequest(t, "/extract", "waiter.pdf", []byte("%PDF-1.7\nwaiter")))
	elapsed := time.Since(start)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after the admission window elapses", rec.Code)
	}
	if elapsed > 2*time.Second {
		t.Errorf("shed took %s; the admission window is %s", elapsed, admission)
	}
	body := decodeProblem(t, rec)
	if slug, _ := body["type"].(string); slug != errBase+"/"+domain.ErrorTypeOverloaded {
		t.Errorf("problem type = %v, want %s/%s", body["type"], errBase, domain.ErrorTypeOverloaded)
	}
}

// TestExtract_QueuedBurstAbsorbsBeyondMaxInFlight is the payoff of taking the
// queue place before the body is read: QUEUE_SIZE can exceed MAX_IN_FLIGHT and
// still absorb a burst, because parked requests hold no PDF bytes. The RAM
// bound stays MAX_IN_FLIGHT.
func TestExtract_QueuedBurstAbsorbsBeyondMaxInFlight(t *testing.T) {
	proc := &holdProcessor{release: make(chan struct{})}

	cfg := testConfig()
	cfg.QueueSize = 20
	cfg.MaxInFlight = 2 // deliberately far below QueueSize
	cfg.AdmissionTimeout = 2 * time.Second
	cfg.ExtractionTimeout = 30 * time.Second

	uc := application.NewExtractTextUseCase(proc, noopRepo{}, nil, application.Limits{
		Workers:   1,
		Admission: cfg.AdmissionTimeout,
	})
	router := newTestRouter(t, uc, &stubPinger{}, cfg)

	const burst = 10
	codes := make([]int, burst)
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := uploadRequest(t, "/extract", "b.pdf",
				[]byte("%PDF-1.7\ndoc "+strconv.Itoa(i)))
			codes[i] = perform(t, router, req).Code
		}(i)
	}

	// Let the burst settle, then release the held slot so nothing is left
	// parked when the test returns.
	time.Sleep(500 * time.Millisecond)
	close(proc.release)
	wg.Wait()

	absorbed, shed := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			absorbed++
		case http.StatusTooManyRequests:
			shed++
		default:
			t.Errorf("unexpected status %d; the burst should either be absorbed or shed with 429", c)
		}
	}
	// A queue of 20 with MAX_IN_FLIGHT=2 must absorb at least the requests that
	// fit its capacity; it is not allowed to refuse the entire burst.
	if absorbed == 0 {
		t.Errorf("all %d requests were shed (%d of them); a queue of %d with 2 read slots should absorb part of the burst",
			burst, shed, cfg.QueueSize)
	}
	t.Logf("burst of %d: absorbed=%d shed429=%d", burst, absorbed, shed)
}
