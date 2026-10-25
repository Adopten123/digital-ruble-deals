package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	service *Service
	logger  *slog.Logger
}

func NewWorker(service *Service, loggers ...*slog.Logger) *Worker {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &Worker{service: service, logger: logger}
}
func (w *Worker) Run(ctx context.Context) error {
	if w.service == nil || w.service.repo == nil {
		return errors.New("notification worker dependencies unavailable")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	failed := false
	var lastWarning time.Time
	for {
		for i := 0; i < 20 && ctx.Err() == nil; i++ {
			batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			worked, err := w.service.DeliverOnce(batchCtx)
			cancel()
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				if !failed || time.Since(lastWarning) >= time.Minute {
					category := "storage"
					if errors.Is(err, context.DeadlineExceeded) {
						category = "timeout"
					}
					// Database errors can contain SQL arguments, payloads or connection data.
					// Log only a fixed classification, never the underlying error string.
					w.logger.Warn("notification delivery deferred; retrying", "worker", "notifications", "category", category)
					lastWarning = time.Now()
				}
				failed = true
				break
			}
			if failed {
				w.logger.Info("notification delivery recovered", "worker", "notifications")
				failed = false
			}
			if !worked {
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
