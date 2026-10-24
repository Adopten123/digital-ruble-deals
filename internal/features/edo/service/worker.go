package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Response struct {
	Status int
	Body   []byte
}
type Sender interface {
	Send(context.Context, Headers, []byte) (Response, error)
}
type Worker struct {
	service *Service
	sender  Sender
}

func NewWorker(s *Service, sender Sender) *Worker { return &Worker{service: s, sender: sender} }

// Run retries durable work. A request or process failure never proves that the
// receiver did not commit; the next attempt uses the same bytes/event ID.
func (w *Worker) Run(ctx context.Context) error {
	if w.service == nil || w.sender == nil {
		return errors.New("EDO worker dependencies unavailable")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for i := 0; i < 20 && ctx.Err() == nil; i++ {
			worked, err := w.DeliverOnce(ctx)
			if err != nil || !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (w *Worker) DeliverOnce(ctx context.Context) (bool, error) {
	job, err := w.service.repo.Claim(ctx)
	if err != nil || job == nil {
		return false, err
	}
	headers := w.service.Sign(job.Raw, time.Now())
	if job.Template == "invalid_hmac" {
		headers.Signature = "sha256=" + strings.Repeat("0", 64)
	}
	response, sendErr := w.sender.Send(ctx, headers, job.Raw)
	terminal := sendErr == nil && response.Status >= 200 && response.Status < 500
	if sendErr != nil {
		response = Response{}
	}
	// Completion needs its own bounded context even after the network deadline;
	// if shutdown interrupts this write the lease expires and the job is retried.
	if err = w.service.repo.Complete(ctx, *job, response.Status, response.Body, terminal); err != nil {
		return true, err
	}
	return true, nil
}
