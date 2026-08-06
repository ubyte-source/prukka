package lane

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
)

type mtWarmer interface {
	Warm(ctx context.Context, from, to core.Lang) error
}

type ttsWarmer interface {
	Warm(ctx context.Context, lang core.Lang, voice core.Voice) error
}

// warmProviders pays MT and TTS model initialization, concurrently, before
// capture starts, so a lane's first committed clause never waits on a model
// load.
func warmProviders(
	ctx context.Context, timeout time.Duration, pool *dispatch.Pool, s *session.Session,
	translator mtWarmer, synth ttsWarmer, voices []core.Voice, startup *startupObserver,
) error {
	warmCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mtTasks := mtWarmTasks(s, translator)
	ttsTasks := ttsWarmTasks(s, synth, voices)
	warmStarted := startup.begin(
		"providers_warming", slog.Int("mt_tasks", len(mtTasks)), slog.Int("tts_tasks", len(ttsTasks)),
	)
	tasks := make([]warmTask, 0, len(mtTasks)+len(ttsTasks))
	tasks = append(tasks, mtTasks...)
	tasks = append(tasks, ttsTasks...)
	if err := runWarmTasks(warmCtx, pool, tasks); err != nil {
		startup.complete("providers_failed", warmStarted)

		return fmt.Errorf("warm providers: %w", err)
	}
	startup.complete("providers_ready", warmStarted)

	return nil
}

type warmTask func(context.Context) error

func mtWarmTasks(s *session.Session, translator mtWarmer) []warmTask {
	tasks := make([]warmTask, 0, len(s.Langs))
	source := s.SourceLang
	seenPairs := map[realtime.LanguagePair]bool{}
	if source != core.LangAuto {
		for _, target := range s.Langs {
			from, to := source.Base(), target.Base()
			route := realtime.LanguagePair{From: from, To: to}
			if from == to || seenPairs[route] {
				continue
			}
			seenPairs[route] = true
			tasks = append(tasks, func(ctx context.Context) error {
				return translator.Warm(ctx, from, to)
			})
		}
	}

	return tasks
}

func ttsWarmTasks(s *session.Session, synth ttsWarmer, voices []core.Voice) []warmTask {
	if synth == nil {
		return nil
	}

	tasks := make([]warmTask, 0, len(voices))
	seenVoices := map[string]bool{}
	for _, target := range supportedWarmVoices(s, voices) {
		voice, _ := voiceForTarget(voices, target)
		if seenVoices[voice.ID] {
			continue
		}
		seenVoices[voice.ID] = true
		tasks = append(tasks, func(ctx context.Context) error {
			return synth.Warm(ctx, target, voice)
		})
	}

	return tasks
}

// runWarmTasks sends model initialization through the same bounded pool as live
// MT/TTS work, so no more than the worker count load models at once.
func runWarmTasks(ctx context.Context, pool *dispatch.Pool, tasks []warmTask) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		pending  sync.WaitGroup
		failOnce sync.Once
		firstErr error
	)
	recordFailure := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for _, task := range tasks {
		pending.Add(1)
		err := pool.Submit(taskCtx, func() {
			defer pending.Done()
			recordFailure(task(taskCtx))
		})
		if err != nil {
			pending.Done()
			recordFailure(err)

			break
		}
	}
	pending.Wait()

	return firstErr
}
