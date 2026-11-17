package jobs

import (
	"context"
	"errors"
	"log"
	"time"
)

const (
	harvestHour   = 6
	harvestMinute = 0
	runTimeout    = 60 * time.Minute
)

type Harvester interface {
	HarvestAll(ctx context.Context) (int, error)
}

// HarvestJob harvest direct na startup alle bronnen en daarna dagelijks om 06:00.
type HarvestJob struct {
	harvester Harvester
	location  *time.Location
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewHarvestJob(parent context.Context, h Harvester) *HarvestJob {
	if h == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	job := &HarvestJob{harvester: h, location: time.Local, ctx: ctx, cancel: cancel}
	go func() {
		job.runOnce()
		job.loop()
	}()
	return job
}

func (j *HarvestJob) Stop() {
	if j != nil && j.cancel != nil {
		j.cancel()
	}
}

func (j *HarvestJob) loop() {
	for {
		timer := time.NewTimer(time.Until(nextRunAt(time.Now().In(j.location), harvestHour, harvestMinute)))
		select {
		case <-j.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			j.runOnce()
		}
	}
}

func (j *HarvestJob) runOnce() {
	ctx, cancel := context.WithTimeout(j.ctx, runTimeout)
	defer cancel()
	count, err := j.harvester.HarvestAll(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[harvest-job] run afgebroken: %v", err)
		} else {
			log.Printf("[harvest-job] run mislukt: %v", err)
		}
		return
	}
	log.Printf("[harvest-job] run gereed; %d skills opgeslagen", count)
}

func nextRunAt(now time.Time, hour, minute int) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}
