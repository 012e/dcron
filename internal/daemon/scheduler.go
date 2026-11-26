package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/schedule"
)

// JobSpec describes a job to register with a Scheduler.
type JobSpec struct {
	Tag      string
	Name     string
	Schedule schedule.Schedule
	Overlap  config.OverlapPolicy
	RunAt    time.Time
}

// JobHandle exposes runtime timing info for a scheduled job.
type JobHandle interface {
	NextRun() (time.Time, error)
	LastRun() (time.Time, error)
}

// Scheduler abstracts the underlying job scheduler (implemented by gocron/v2).
type Scheduler interface {
	Start()
	Shutdown() error
	ScheduleJob(spec JobSpec, task func(ctx context.Context)) (JobHandle, error)
	RemoveByTag(tag string) error
}

// GoCronScheduler adapts github.com/go-co-op/gocron/v2 to the Scheduler interface.
type GoCronScheduler struct {
	sched  gocron.Scheduler
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewGoCronScheduler constructs a GoCronScheduler in the local timezone (or loc if provided).
func NewGoCronScheduler(loc *time.Location) (*GoCronScheduler, error) {
	if loc == nil {
		loc = time.Local
	}
	s, err := gocron.NewScheduler(gocron.WithLocation(loc))
	if err != nil {
		return nil, fmt.Errorf("create gocron scheduler: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &GoCronScheduler{
		sched:  s,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

func (g *GoCronScheduler) Start() {
	g.sched.Start()
}

func (g *GoCronScheduler) Shutdown() error {
	g.cancel()
	return g.sched.Shutdown()
}

func (g *GoCronScheduler) ScheduleJob(spec JobSpec, task func(ctx context.Context)) (JobHandle, error) {
	var def gocron.JobDefinition
	switch spec.Schedule.Kind {
	case schedule.KindCron:
		def = gocron.CronJob(spec.Schedule.CronExpr, false)
	case schedule.KindInterval:
		def = gocron.DurationJob(spec.Schedule.Interval)
	case schedule.KindReboot:
		runAt := spec.RunAt
		if runAt.IsZero() && spec.Schedule.RebootDelay > 0 {
			runAt = time.Now().Add(spec.Schedule.RebootDelay)
		}
		if !runAt.IsZero() && runAt.After(time.Now()) {
			def = gocron.OneTimeJob(gocron.OneTimeJobStartDateTime(runAt))
		} else {
			def = gocron.OneTimeJob(gocron.OneTimeJobStartImmediately())
		}
	default:
		return nil, fmt.Errorf("unsupported schedule kind %v", spec.Schedule.Kind)
	}

	opts := []gocron.JobOption{
		gocron.WithName(spec.Name),
	}
	if spec.Tag != "" {
		opts = append(opts, gocron.WithTags(spec.Tag))
	}

	switch spec.Overlap {
	case config.OverlapSkip, "":
		opts = append(opts, gocron.WithSingletonMode(gocron.LimitModeReschedule))
	case config.OverlapWait:
		opts = append(opts, gocron.WithSingletonMode(gocron.LimitModeWait))
	case config.OverlapAllow:
		// No singleton limit.
	}

	taskFn := gocron.NewTask(func() {
		g.mu.Lock()
		ctx := g.ctx
		g.mu.Unlock()
		task(ctx)
	})

	j, err := g.sched.NewJob(def, taskFn, opts...)
	if err != nil && spec.Schedule.Kind == schedule.KindReboot && errors.Is(err, gocron.ErrOneTimeJobStartDateTimePast) {
		j, err = g.sched.NewJob(gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()), taskFn, opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("schedule job %q: %w", spec.Name, err)
	}
	return j, nil
}

func (g *GoCronScheduler) RemoveByTag(tag string) error {
	g.sched.RemoveByTags(tag)
	return nil
}
