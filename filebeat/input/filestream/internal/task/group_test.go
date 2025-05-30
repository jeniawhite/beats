// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package task

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopLogger struct{}

func (n noopLogger) Errorf(string, ...interface{}) {}

type testLogger struct {
	mu sync.Mutex
	b  strings.Builder
}

func (tl *testLogger) Errorf(format string, args ...interface{}) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.b.WriteString(fmt.Sprintf(format, args...))
	tl.b.WriteString("\n")
}
func (tl *testLogger) String() string {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return tl.b.String()
}

func TestNewGroup(t *testing.T) {
	limit := 10
	timeout := time.Second
	g := NewGroup(uint64(limit), timeout, noopLogger{}, "")
	require.NotNil(t, g, "NewGroup returned a nil group, it cannot be nil")

	require.NotNil(t, g.sem)

	err := g.sem.Acquire(context.Background(), int64(limit-1))
	require.NoError(t, err, "semaphore Acquire failed")
	assert.True(t, g.sem.TryAcquire(1),
		"semaphore should have 1 place left, there is none")
	assert.False(t, g.sem.TryAcquire(1),
		"semaphore NOT should have any place left, but there is")

	assert.NotNil(t, g.logErr)
	assert.Equal(t, timeout, g.stopTimeout)
}

func TestGroup_Go(t *testing.T) {
	t.Run("don't run more than limit goroutines", func(t *testing.T) {
		done := make(chan struct{})
		defer close(done)
		runningCount := atomic.Uint64{}
		blocked := func(_ context.Context) error {
			runningCount.Add(1)
			<-done
			return nil
		}

		want := uint64(2)
		g := NewGroup(want, time.Second, noopLogger{}, "")

		err := g.Go(blocked)
		require.NoError(t, err)
		err = g.Go(blocked)
		require.NoError(t, err)
		err = g.Go(blocked)
		require.NoError(t, err)

		assert.Eventually(t,
			func() bool { return want == runningCount.Load() },
			1*time.Second, 10*time.Millisecond)
	})

	t.Run("workloads wait for available worker", func(t *testing.T) {
		runningCount := atomic.Int64{}
		doneCount := atomic.Int64{}

		limit := uint64(2)
		g := NewGroup(limit, time.Second, noopLogger{}, "")

		done1 := make(chan struct{})
		f1 := func(_ context.Context) error {
			defer t.Log("f1 done")
			defer doneCount.Add(1)

			runningCount.Add(1)
			defer runningCount.Add(-1)

			t.Log("f1 started")
			<-done1
			return errors.New("f1")
		}

		var f2Finished atomic.Bool
		done2 := make(chan struct{})
		f2 := func(_ context.Context) error {
			defer t.Log("f2 done")
			defer doneCount.Add(1)

			runningCount.Add(1)

			t.Log("f2 started")
			<-done2

			f2Finished.Store(true)

			runningCount.Add(-1)
			return errors.New("f2")
		}

		var f3Started atomic.Bool
		done3 := make(chan struct{})
		f3 := func(_ context.Context) error {
			defer t.Log("f3 done")
			defer doneCount.Add(1)

			f3Started.Store(true)
			runningCount.Add(1)

			defer runningCount.Add(-1)
			t.Log("f3 started")
			<-done3
			return errors.New("f3")
		}

		err := g.Go(f1)
		require.NoError(t, err)
		err = g.Go(f2)
		require.NoError(t, err)

		// Wait to ensure f1 and f2 are running, thus there is no workers free.
		assert.Eventually(t,
			func() bool { return int64(2) == runningCount.Load() },
			1*time.Second, 10*time.Millisecond)

		err = g.Go(f3)
		require.NoError(t, err)
		assert.False(t, f3Started.Load())

		close(done2)

		assert.Eventually(t,
			func() bool {
				return f3Started.Load()
			},
			1*time.Second, 10*time.Millisecond)

		// If f3 started, f2 must have finished
		assert.True(t, f2Finished.Load())
		assert.Equal(t, int64(limit), runningCount.Load())

		close(done1)
		close(done3)

		t.Log("waiting the worker pool to finish all workloads")
		err = g.Stop()
		assert.NoError(t, err)
		t.Log("worker pool to finished all workloads")

		assert.Eventually(t,
			func() bool { return doneCount.Load() == 3 },
			1*time.Second,
			10*time.Millisecond,
			"not all goroutines finished")
	})

	t.Run("return error if the group is closed", func(t *testing.T) {
		g := NewGroup(1, time.Second, noopLogger{}, "")
		err := g.Stop()
		require.NoError(t, err)

		err = g.Go(func(_ context.Context) error { return nil })
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("without limit, all goroutines run", func(t *testing.T) {
		// 100 <= limit <= 10000
		limit := rand.IntN(10000-100) + 100
		t.Logf("running %d goroutines", limit)
		g := NewGroup(uint64(limit), time.Second, noopLogger{}, "")

		done := make(chan struct{})
		var runningCounter atomic.Int64
		for i := 0; i < limit; i++ {
			err := g.Go(func(context.Context) error {
				runningCounter.Add(1)
				defer runningCounter.Add(-1)

				<-done
				return nil
			})
			require.NoError(t, err)
		}

		assert.Eventually(t,
			func() bool { return int64(limit) == runningCounter.Load() },
			1*time.Second,
			10*time.Millisecond)

		close(done)
		err := g.Stop()
		require.NoError(t, err)
	})

	t.Run("all workloads return an error", func(t *testing.T) {
		logger := &testLogger{}
		var count atomic.Uint64

		wantErr := errors.New("a error")
		workload := func(i int) func(context.Context) error {
			return func(_ context.Context) error {
				defer count.Add(1)
				return fmt.Errorf("[%d]: %w", i, wantErr)
			}
		}

		want := uint64(2)
		g := NewGroup(want, time.Second, logger, "errorPrefix")

		err := g.Go(workload(1))
		require.NoError(t, err)

		err = g.Go(workload(2))
		require.NoError(t, err)

		assert.Eventually(t, func() bool {
			return count.Load() == want && logger.String() != ""
		}, 1*time.Second, 10*time.Millisecond)

		err = g.Stop()
		require.NoError(t, err)

		logs := logger.String()
		assert.Contains(t, logs, wantErr.Error())
		assert.Contains(t, logs, "[2]")
		assert.Contains(t, logs, "[1]")

	})

	t.Run("some workloads return an error", func(t *testing.T) {
		wantErr := errors.New("a error")
		logger := &testLogger{}
		want := uint64(2)

		g := NewGroup(want, time.Second, logger, "")

		var count atomic.Uint64
		err := g.Go(func(_ context.Context) error {
			count.Add(1)
			return nil
		})
		require.NoError(t, err)
		err = g.Go(func(_ context.Context) error {
			count.Add(1)
			return wantErr
		})
		require.NoError(t, err)

		assert.Eventually(t, func() bool {
			return count.Load() == want && logger.String() != ""
		}, 1*time.Second, 10*time.Millisecond, "not all workloads finished")

		assert.Contains(t, logger.String(), wantErr.Error())

		err = g.Stop()
		assert.NoError(t, err)
	})

	t.Run("workload returns no error", func(t *testing.T) {
		done := make(chan struct{})
		runningCount := atomic.Uint64{}
		wg := sync.WaitGroup{}

		bloked := func(i int) func(context.Context) error {
			return func(_ context.Context) error {
				runningCount.Add(1)
				defer wg.Done()

				<-done
				return nil
			}
		}

		want := uint64(2)
		g := NewGroup(want, time.Second, noopLogger{}, "")

		wg.Add(2)
		err := g.Go(bloked(1))
		require.NoError(t, err)
		err = g.Go(bloked(2))
		require.NoError(t, err)

		close(done)
		wg.Wait()

		err = g.Stop()

		assert.NoError(t, err)
	})
}

func TestGroup_Stop(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		g := NewGroup(50, time.Millisecond, noopLogger{}, "")

		done := make(chan struct{})
		wg := sync.WaitGroup{}
		wg.Add(1)
		defer func() { close(done) }()
		err := g.Go(func(_ context.Context) error {
			wg.Done() // signal that the goroutine is running
			<-done
			return nil
		})
		require.NoError(t, err, "could not launch goroutine")
		wg.Wait() // wait for the goroutine to start

		err = g.Stop()
		require.NotNil(t, err, "Stop should return a timeout error")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("all goroutine finish before timeout", func(t *testing.T) {
		g := NewGroup(1, 50*time.Millisecond, noopLogger{}, "")

		err := g.Go(func(_ context.Context) error { return nil })
		require.NoError(t, err, "could not launch goroutine")

		err = g.Stop()
		assert.NoError(t, err)
	})
}
