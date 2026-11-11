package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/evals"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// defaultEvalConfig is used when --config is not given: the eval must not depend on the
// operator's sentinela.yaml (repos, teams, Linear) and must score the shipped defaults.
const defaultEvalConfig = "evals/config.yaml"

func newEvalCmd(d *deps) *cobra.Command {
	var dataset, recordings, out, provider, check string
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score the Judge against the labeled dataset in evals/ (no database needed)",
		Long: `Runs the Judge on every case of the dataset, applies the same decision rule as discovery
(threshold and re-votes from the config) and writes evals/results/latest.{json,md}.

Live run (spends model calls): sentinela eval
Replay (no model calls, as in CI): sentinela eval --provider replay --check evals/results/latest.json

A live run saves every response under --recordings; the replay answers from them and fails when
the prompt, the schema or the dataset changed since the live run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath := defaultEvalConfig
			if f := cmd.Flag("config"); f != nil && f.Changed {
				cfgPath = *d.cfgPath
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			if provider != "" {
				cfg.LLM.Provider = provider
			}
			ds, err := evals.Load(dataset)
			if err != nil {
				return fmt.Errorf("dataset %s: %w", dataset, err)
			}
			settings := evals.SettingsFrom(cfg)
			rec := &evals.MemoryRecorder{}
			caller, err := evalCaller(d, cfg, rec, recordings)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "judge eval: %d cases, model %s, provider %s\n", len(ds.Cases), settings.Model, caller.Provider())
			results, err := evals.Run(cmd.Context(), caller, ds, settings)
			if err != nil {
				return err
			}
			rep := evals.NewReport(time.Now(), caller.Provider(), judge.PromptVersion().Version, ds, settings, results)
			if caller.Provider() != "replay" {
				calls, usage, cost := rec.Totals()
				rep.Cost = &evals.Cost{Calls: calls, InputTokens: usage.PromptTokens(), OutputTokens: usage.OutputTokens, USD: cost.Dollars()}
			}
			if _, err := fmt.Fprint(w, rep.Markdown()); err != nil {
				return err
			}

			if check != "" {
				return checkReport(check, rep)
			}
			if caller.Provider() == "replay" {
				return nil // a replay never overwrites the recorded results
			}
			return writeReport(out, rep)
		},
	}
	cmd.Flags().StringVar(&dataset, "dataset", "evals/dataset/findings.jsonl", "labeled dataset (JSONL)")
	cmd.Flags().StringVar(&recordings, "recordings", "evals/recordings", "where a live run saves responses and a replay reads them")
	cmd.Flags().StringVar(&out, "out", "evals/results", "directory for latest.json and latest.md")
	cmd.Flags().StringVar(&provider, "provider", "", "override llm.provider: anthropic_api | claude_code | replay")
	cmd.Flags().StringVar(&check, "check", "", "compare with a recorded report (JSON) and fail on any difference")
	return cmd
}

func evalCaller(d *deps, cfg *config.Config, rec llm.Recorder, recordings string) (llm.Caller, error) {
	if cfg.LLM.Provider == config.ProviderReplay {
		return &evals.Replayer{Dir: recordings, B: llm.NewCallBudget(0)}, nil
	}
	prices := llm.NewPriceTable(cfg.Pricing)
	var (
		inner llm.Caller
		err   error
	)
	switch cfg.LLM.Provider {
	case config.ProviderAnthropicAPI:
		key := d.env("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, errors.New("ANTHROPIC_API_KEY is not set")
		}
		inner, err = llm.NewClient(llm.Options{
			API: anthropicClient(d, key), Prices: prices, Recorder: rec,
			Budget: llm.NewBudget(llm.USD(cfg.Budget.PerRunUSD)), Sync: !cfg.Models.UseBatch,
			PollInterval: cfg.Discovery.BatchPollInterval,
		})
	case config.ProviderClaudeCode:
		cc := cfg.LLM.ClaudeCode
		if err := os.MkdirAll(cc.WorkDir, 0o750); err != nil {
			return nil, err
		}
		inner, err = llm.NewClaudeCode(llm.ClaudeCodeOptions{
			Binary: cc.Binary, Concurrency: cc.Concurrency, Timeout: cc.Timeout, WorkDir: cc.WorkDir,
			Recorder: rec, Budget: llm.NewCallBudget(cc.MaxCallsPerRun), Prices: prices,
		})
	default:
		return nil, fmt.Errorf("unknown llm.provider %q", cfg.LLM.Provider)
	}
	if err != nil {
		return nil, err
	}
	return &evals.Recorder{Caller: inner, Dir: recordings}, nil
}

func writeReport(dir string, rep evals.Report) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "latest.md"), []byte(rep.Markdown()), 0o600)
}

func checkReport(path string, got evals.Report) error {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return err
	}
	var want evals.Report
	if err := json.Unmarshal(data, &want); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if diffs := evals.Compare(want, got); len(diffs) > 0 {
		return fmt.Errorf("replay does not reproduce %s (rerun the live eval):\n  %s", path, strings.Join(diffs, "\n  "))
	}
	return nil
}
