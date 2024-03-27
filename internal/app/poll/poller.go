// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package poll

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"connectrpc.com/connect"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"

	"gitea.com/gitea/act_runner/internal/app/run"
	"gitea.com/gitea/act_runner/internal/pkg/client"
	"gitea.com/gitea/act_runner/internal/pkg/config"
)

type Poller struct {
	client       client.Client
	runner       *run.Runner
	cfg          *config.Config
	tasksVersion atomic.Int64 // tasksVersion used to store the version of the last task fetched from the Gitea.
}

func New(cfg *config.Config, client client.Client, runner *run.Runner) *Poller {
	return &Poller{
		client: client,
		runner: runner,
		cfg:    cfg,
	}
}

func (p *Poller) Poll(ctx context.Context) {
	limiter := rate.NewLimiter(rate.Every(p.cfg.Runner.FetchInterval), 1)
	wg := &sync.WaitGroup{}
	for i := 0; i < p.cfg.Runner.Capacity; i++ {
		wg.Add(1)
		go p.poll(ctx, wg, limiter)
	}
	wg.Wait()
}

func (p *Poller) poll(ctx context.Context, wg *sync.WaitGroup, limiter *rate.Limiter) {
	defer wg.Done()
	for {
		if err := limiter.Wait(ctx); err != nil {
			if ctx.Err() != nil {
				log.WithError(err).Debug("limiter wait failed")
			}
			return
		}
		task, ok := p.fetchTask(ctx)
		if !ok {
			continue
		}
		p.runTaskWithRecover(ctx, task)
	}
}

func (p *Poller) runTaskWithRecover(ctx context.Context, task *runnerv1.Task) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic: %v", r)
			log.WithError(err).Error("panic in runTaskWithRecover")
		}
	}()

	if err := p.runner.Run(ctx, task); err != nil {
		log.WithError(err).Error("failed to run task")
	}
}

func (p *Poller) fetchTask(ctx context.Context) (*runnerv1.Task, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Runner.FetchTimeout)
	defer cancel()

	// Load the version value that was in the cache when the request was sent.
	v := p.tasksVersion.Load()
	resp, err := p.client.FetchTask(reqCtx, connect.NewRequest(&runnerv1.FetchTaskRequest{
		TasksVersion: v,
	}))
	if errors.Is(err, context.DeadlineExceeded) {
		err = nil
	}
	if err != nil {
		log.WithError(err).Error("failed to fetch task")
		return nil, false
	}

	if resp == nil || resp.Msg == nil {
		return nil, false
	}

	if resp.Msg.TasksVersion > v {
		p.tasksVersion.CompareAndSwap(v, resp.Msg.TasksVersion)
	}

	if resp.Msg.Task == nil {
		return nil, false
	}

	// got a task, set `tasksVersion` to zero to focre query db in next request.
	p.tasksVersion.CompareAndSwap(resp.Msg.TasksVersion, 0)

	return resp.Msg.Task, true
}
