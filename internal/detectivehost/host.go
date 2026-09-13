package detectivehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detective"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options controls explicit startup operations for the standalone host.
type Options struct {
	ApplyMigrations bool
	Logger          *slog.Logger
	PlannerRunner   detective.PlannerRunner
}

type maintenanceController struct {
	hostID     string
	bootID     string
	actorID    string
	batchLimit int
	sequence   uint64
}

type maintenanceOutcome struct {
	expiredRequestID   string
	retryRequestID     string
	expiredTransitions int
	retryConsumptions  int
}

// Run owns one foreground detective host until its context is cancelled.
func Run(ctx context.Context, databaseURL string, config Config, options Options) error {
	if ctx == nil {
		return errors.New("detective host context is required")
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := config.validate(); err != nil {
		return err
	}
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return errors.New("DATABASE_DNS is required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parsing postgres configuration: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return contextAwareError(ctx, "opening postgres pool", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return contextAwareError(ctx, "pinging postgres", err)
	}

	if options.ApplyMigrations {
		changed, err := migrations.ApplyUp(ctx, pool)
		if err != nil {
			return contextAwareError(ctx, "applying migrations", err)
		}
		logger.Info("database migrations checked", "event", "migrations_checked", "changed", changed)
	}
	schemaStatus, err := migrations.VerifyCurrent(ctx, pool)
	if err != nil {
		return contextAwareError(ctx, "verifying database schema", err)
	}
	logger.Info(
		"database schema verified",
		"event", "schema_verified",
		"applied_migrations", schemaStatus.AppliedMigrations,
		"latest_migration", schemaStatus.LatestMigration,
	)

	registration, err := detective.RegisterWorkspace(ctx, pool, detective.WorkspaceRegistrationInput{
		RequestID:     config.RegistrationRequestID,
		WorkspaceID:   config.WorkspaceID,
		WorkspaceRoot: config.WorkspaceRoot,
		Sources:       config.workspaceSources(),
	})
	if err != nil {
		return contextAwareError(ctx, "registering detective workspace", err)
	}
	logger.Info(
		"detective workspace ready",
		"event", "workspace_ready",
		"host_id", config.HostID,
		"workspace_id", registration.Workspace.ID,
		"source_count", len(registration.Workspace.Sources),
		"replayed", registration.Replayed,
	)

	localSourceBindingIDs := make([]string, 0, len(config.Sources))
	for _, configured := range config.Sources {
		source, found := registeredSource(
			registration.Workspace,
			configured.CapabilityName,
			configured.CapabilityVersion,
			configured.SourceID,
		)
		if !found {
			return fmt.Errorf(
				"registered local source %s/%s/%s was not found",
				configured.CapabilityName,
				configured.CapabilityVersion,
				configured.SourceID,
			)
		}
		localSourceBindingIDs = append(localSourceBindingIDs, source.ID)
	}
	mcpRuntimeSources := make([]detective.MCPReadPeriodicSourceConfig, 0, len(config.MCPReadSources))
	for _, configured := range config.MCPReadSources {
		source, found := registeredSource(
			registration.Workspace,
			detective.SourceCapabilityMCPReadDocument,
			detective.SourceCapabilityMCPReadDocumentVersion,
			configured.SourceID,
		)
		if !found {
			return fmt.Errorf("registered MCP read source %s was not found", configured.SourceID)
		}
		binding, replayed, err := detective.RegisterMCPReadSourceBinding(
			ctx,
			pool,
			detective.MCPReadSourceBindingInput{
				WorkspaceID:     registration.Workspace.ID,
				SourceBindingID: source.ID,
				Config:          configured.detectiveConfig(),
			},
		)
		if err != nil {
			return contextAwareError(ctx, "registering MCP read source binding", err)
		}
		logger.Info(
			"MCP read source ready",
			"event", "mcp_read_source_ready",
			"host_id", config.HostID,
			"source_binding_id", binding.SourceBindingID,
			"source_id", binding.SourceID,
			"binding_hash", binding.BindingHash,
			"replayed", replayed,
		)
		proposalExtractor, err := newMCPProposalExtractor(configured.ProposalExtraction)
		if err != nil {
			return contextAwareError(ctx, "configuring MCP proposal extractor", err)
		}
		if configured.ProposalConversion {
			converted, found, err := detective.ConvertLatestMCPReadSourceSnapshot(
				ctx,
				pool,
				registration.Workspace.ID,
				source.ID,
			)
			if err != nil {
				return contextAwareError(ctx, "recovering MCP document proposal conversion", err)
			}
			if found {
				logMCPReadProposalConversion(
					logger,
					config.HostID,
					source.ID,
					converted,
					true,
				)
			}
		}
		if proposalExtractor != nil {
			extracted, found, err := detective.ExtractLatestMCPReadSourceSnapshot(
				ctx,
				pool,
				registration.Workspace.ID,
				source.ID,
				detective.MCPReadProposalExtractionInput{
					MaxProposals:        proposalExtractor.MaxProposals,
					SectionMode:         proposalExtractor.SectionMode,
					MaxSections:         proposalExtractor.MaxSections,
					ExtractorDefinition: proposalExtractor.ExtractorDefinition,
					Runner:              proposalExtractor.Runner,
				},
			)
			if err != nil {
				return contextAwareError(ctx, "recovering MCP document proposal extraction", err)
			}
			if found {
				logMCPReadProposalExtraction(
					logger,
					config.HostID,
					source.ID,
					extracted,
					true,
				)
			}
		}
		mcpRuntimeSources = append(mcpRuntimeSources, detective.MCPReadPeriodicSourceConfig{
			SourceBindingID:    source.ID,
			Config:             configured.detectiveConfig(),
			ProposalConversion: configured.ProposalConversion,
			ProposalExtractor:  proposalExtractor,
		})
	}

	var maintenance *maintenanceController
	if config.RepositoryMaintenance.Enabled {
		maintenance, err = newMaintenanceController(config.HostID, config.RepositoryMaintenance.BatchLimit)
		if err != nil {
			return err
		}
		outcome, err := maintenance.run(ctx, pool)
		if err != nil {
			return contextAwareError(ctx, "running startup repository maintenance", err)
		}
		logMaintenanceOutcome(logger, config.HostID, outcome, true)
	}

	periodicConfig := detective.PeriodicRuntimeConfig{
		WorkspaceID:      config.WorkspaceID,
		MaxSteps:         config.MaxSteps,
		Interval:         config.SourceInterval,
		SourceBindingIDs: localSourceBindingIDs,
	}
	var periodic *detective.PeriodicRuntime
	if len(localSourceBindingIDs) != 0 {
		if config.Planner.Enabled {
			runner := options.PlannerRunner
			if runner == nil {
				ollama, err := detective.NewOllamaPlannerRunner(detective.OllamaPlannerConfig{
					BaseURL:    config.Planner.BaseURL,
					Model:      config.Planner.Model,
					Timeout:    config.Planner.Timeout,
					NumPredict: config.Planner.NumPredict,
				})
				if err != nil {
					return contextAwareError(ctx, "configuring bounded planner", err)
				}
				runner = ollama.Run
			}
			periodic, err = detective.NewPlannerPeriodicRuntime(ctx, pool, periodicConfig, runner)
		} else {
			if options.PlannerRunner != nil {
				return errors.New("planner runner requires planner.enabled=true")
			}
			periodic, err = detective.NewPeriodicRuntime(ctx, pool, periodicConfig)
		}
	} else {
		if options.PlannerRunner != nil {
			return errors.New("planner runner requires planner.enabled=true")
		}
	}
	if err != nil {
		return contextAwareError(ctx, "starting periodic source runtime", err)
	}
	var periodicOutcomes <-chan detective.PeriodicRuntimeOutcome
	if periodic != nil {
		periodicOutcomes = periodic.Outcomes()
		defer periodic.Close()
	}

	var mcpPeriodic *detective.MCPReadPeriodicRuntime
	var mcpPeriodicOutcomes <-chan detective.MCPReadPeriodicRuntimeOutcome
	if len(mcpRuntimeSources) != 0 {
		mcpPeriodic, err = detective.NewMCPReadPeriodicRuntime(ctx, pool, detective.MCPReadPeriodicRuntimeConfig{
			WorkspaceID: config.WorkspaceID,
			Interval:    config.SourceInterval,
			Sources:     mcpRuntimeSources,
		})
		if err != nil {
			return contextAwareError(ctx, "starting MCP read periodic runtime", err)
		}
		mcpPeriodicOutcomes = mcpPeriodic.Outcomes()
		defer mcpPeriodic.Close()
	}

	var maintenanceTicker *time.Ticker
	var maintenanceTicks <-chan time.Time
	if maintenance != nil {
		maintenanceTicker = time.NewTicker(config.RepositoryMaintenance.Interval)
		maintenanceTicks = maintenanceTicker.C
		defer maintenanceTicker.Stop()
	}
	logger.Info(
		"detective host started",
		"event", "host_started",
		"host_id", config.HostID,
		"workspace_id", config.WorkspaceID,
		"source_interval", config.SourceInterval.String(),
		"max_steps", config.MaxSteps,
		"repository_maintenance", maintenance != nil,
		"planner_enabled", config.Planner.Enabled,
		"planner_model", config.Planner.Model,
		"local_source_count", len(localSourceBindingIDs),
		"mcp_read_source_count", len(mcpRuntimeSources),
		"mcp_proposal_conversion_source_count", mcpProposalConversionSourceCount(config.MCPReadSources),
		"mcp_proposal_extraction_source_count", mcpProposalExtractionSourceCount(config.MCPReadSources),
	)

	for {
		select {
		case <-ctx.Done():
			logger.Info(
				"detective host stopping",
				"event", "host_stopping",
				"host_id", config.HostID,
				"workspace_id", config.WorkspaceID,
			)
			if periodic != nil {
				periodic.Close()
			}
			if mcpPeriodic != nil {
				mcpPeriodic.Close()
			}
			logger.Info(
				"detective host stopped",
				"event", "host_stopped",
				"host_id", config.HostID,
				"workspace_id", config.WorkspaceID,
			)
			return nil
		case outcome, ok := <-periodicOutcomes:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("periodic source runtime stopped unexpectedly")
			}
			logSourceOutcome(logger, config.HostID, outcome)
		case outcome, ok := <-mcpPeriodicOutcomes:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("MCP read periodic runtime stopped unexpectedly")
			}
			logMCPReadSourceOutcome(logger, config.HostID, outcome)
		case <-maintenanceTicks:
			outcome, err := maintenance.run(ctx, pool)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				logger.Error(
					"repository maintenance failed",
					"event", "repository_maintenance_failed",
					"host_id", config.HostID,
					"error", err,
				)
				continue
			}
			logMaintenanceOutcome(logger, config.HostID, outcome, false)
		}
	}
}

func registeredSource(
	workspace detective.Workspace,
	capabilityName string,
	capabilityVersion string,
	sourceID string,
) (detective.WorkspaceSourceBinding, bool) {
	for _, source := range workspace.Sources {
		if source.CapabilityName == capabilityName &&
			source.CapabilityVersion == capabilityVersion &&
			source.SourceID == sourceID {
			return source, true
		}
	}
	return detective.WorkspaceSourceBinding{}, false
}

func newMaintenanceController(hostID string, batchLimit int) (*maintenanceController, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return nil, fmt.Errorf("creating detective host boot identity: %w", err)
	}
	return &maintenanceController{
		hostID:     hostID,
		bootID:     hex.EncodeToString(data),
		actorID:    "ahe-detective-host:" + hostID,
		batchLimit: batchLimit,
	}, nil
}

func (c *maintenanceController) run(ctx context.Context, pool *pgxpool.Pool) (maintenanceOutcome, error) {
	expiredRequestID := c.nextRequestID("expired")
	expired, err := evidenceingestion.RunExpiredRepositoryExtractionWorkMaintenanceTick(
		ctx,
		pool,
		evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput{
			RequestID:          expiredRequestID,
			Limit:              c.batchLimit,
			MaintenanceActorID: c.actorID,
		},
	)
	if err != nil {
		return maintenanceOutcome{}, err
	}
	retryRequestID := c.nextRequestID("retry")
	retry, err := evidenceingestion.RunRepositoryExtractionWorkRetryControllerTick(
		ctx,
		pool,
		evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput{
			RequestID:       retryRequestID,
			Limit:           c.batchLimit,
			ConsumerActorID: c.actorID,
		},
	)
	if err != nil {
		return maintenanceOutcome{}, err
	}
	return maintenanceOutcome{
		expiredRequestID:   expired.RequestID,
		retryRequestID:     retry.RequestID,
		expiredTransitions: len(expired.Transitions),
		retryConsumptions:  len(retry.Consumptions),
	}, nil
}

func (c *maintenanceController) nextRequestID(kind string) string {
	c.sequence++
	return fmt.Sprintf(
		"ahe-detective-host:%s:%s:%s:%d",
		c.hostID,
		c.bootID,
		kind,
		c.sequence,
	)
}

func logSourceOutcome(logger *slog.Logger, hostID string, outcome detective.PeriodicRuntimeOutcome) {
	if outcome.Err != nil {
		event := "source_tick_failed"
		message := "periodic source tick failed"
		if outcome.Result.PlannerContinuation != nil {
			event = "planner_continuation_failed"
			message = "bounded planner continuation failed"
		}
		logger.Error(
			message,
			"event", event,
			"host_id", hostID,
			"source_binding_id", outcome.SourceBindingID,
			"cycle_id", outcome.Result.Cycle.ID,
			"run_id", outcome.Result.Run.ID,
			"error", outcome.Err,
		)
		return
	}
	logger.Info(
		"periodic source tick completed",
		"event", "source_tick_completed",
		"host_id", hostID,
		"source_binding_id", outcome.SourceBindingID,
		"cycle_id", outcome.Result.Cycle.ID,
		"cycle_status", outcome.Result.Cycle.Status,
		"run_id", outcome.Result.Run.ID,
		"run_status", outcome.Result.Run.Status,
		"replayed", outcome.Result.Replayed,
		"source_invoked", outcome.Result.SourceInvoked,
		"cursor_advanced", outcome.Result.CursorAdvanced,
	)
	continuation := outcome.Result.PlannerContinuation
	if continuation == nil {
		return
	}
	if continuation.Consumption != nil {
		logger.Info(
			"bounded planner recommendation consumed",
			"event", "planner_recommendation_consumed",
			"host_id", hostID,
			"cycle_id", outcome.Result.Cycle.ID,
			"run_id", outcome.Result.Run.ID,
			"planning_context_hash", continuation.Consumption.PlanningContextHash,
			"source_binding_id", continuation.Consumption.Step.SourceBindingID,
			"resumed", continuation.Resumed,
			"source_invoked", continuation.Consumption.SourceInvoked,
		)
		return
	}
	if continuation.Recommendation != nil &&
		continuation.Recommendation.Classification == detective.PlannerClassificationAbstain {
		logger.Info(
			"bounded planner abstained",
			"event", "planner_abstained",
			"host_id", hostID,
			"cycle_id", outcome.Result.Cycle.ID,
			"run_id", outcome.Result.Run.ID,
			"planning_context_hash", continuation.Recommendation.PlanningContextHash,
		)
	}
}

func logMCPReadSourceOutcome(
	logger *slog.Logger,
	hostID string,
	outcome detective.MCPReadPeriodicRuntimeOutcome,
) {
	if outcome.Err != nil {
		logger.Error(
			"MCP read source tick failed",
			"event", "mcp_read_source_tick_failed",
			"host_id", hostID,
			"source_binding_id", outcome.SourceBindingID,
			"cycle_id", outcome.Result.Cycle.ID,
			"cycle_status", outcome.Result.Cycle.Status,
			"failure_kind", outcome.Result.Cycle.FailureKind,
			"error", outcome.Err,
		)
		return
	}
	logger.Info(
		"MCP read source tick completed",
		"event", "mcp_read_source_tick_completed",
		"host_id", hostID,
		"source_binding_id", outcome.SourceBindingID,
		"cycle_id", outcome.Result.Cycle.ID,
		"cycle_number", outcome.Result.Cycle.Number,
		"cycle_status", outcome.Result.Cycle.Status,
		"connector_delivery_id", outcome.Result.Cycle.ConnectorDeliveryID,
		"source_snapshot_id", outcome.Result.Cycle.SourceSnapshotID,
		"provider_revision", outcome.Result.Cycle.ProviderRevision,
		"coverage_complete", outcome.Result.Cycle.CoverageComplete,
		"coverage_truncated", outcome.Result.Cycle.CoverageTruncated,
		"completion_reason", outcome.Result.Cycle.CompletionReason,
		"resumed", outcome.Result.Resumed,
		"execution_deferred", outcome.Result.Deferred,
	)
	if outcome.ConversionErr != nil {
		logger.Error(
			"MCP document proposal conversion failed",
			"event", "mcp_read_proposal_conversion_failed",
			"host_id", hostID,
			"source_binding_id", outcome.SourceBindingID,
			"source_snapshot_id", outcome.Result.Cycle.SourceSnapshotID,
			"error", outcome.ConversionErr,
		)
		return
	}
	if outcome.Conversion != nil {
		logMCPReadProposalConversion(
			logger,
			hostID,
			outcome.SourceBindingID,
			*outcome.Conversion,
			false,
		)
	}
	if outcome.ExtractionErr != nil {
		logger.Error(
			"MCP document proposal extraction failed",
			"event", "mcp_read_proposal_extraction_failed",
			"host_id", hostID,
			"source_binding_id", outcome.SourceBindingID,
			"source_snapshot_id", outcome.Result.Cycle.SourceSnapshotID,
			"error", outcome.ExtractionErr,
		)
		return
	}
	if outcome.Extraction != nil {
		logMCPReadProposalExtraction(
			logger,
			hostID,
			outcome.SourceBindingID,
			*outcome.Extraction,
			false,
		)
	}
}

func logMCPReadProposalConversion(
	logger *slog.Logger,
	hostID string,
	sourceBindingID string,
	result detective.MCPReadProposalConversionResult,
	recovered bool,
) {
	logger.Info(
		"MCP document proposal conversion completed",
		"event", "mcp_read_proposal_conversion_completed",
		"host_id", hostID,
		"source_binding_id", sourceBindingID,
		"source_snapshot_id", result.SourceSnapshotID,
		"extraction_view_id", result.ExtractionViewID,
		"conversion_request_id", result.ConversionRequestID,
		"conversion_status", result.Status,
		"conversion_reason", result.Reason,
		"proposal_count", result.ProposalCount,
		"proposal_occurrence_id", result.ProposalOccurrenceID,
		"replayed", result.Replayed,
		"recovered_at_startup", recovered,
	)
}

func mcpProposalConversionSourceCount(sources []MCPReadSourceConfig) int {
	count := 0
	for _, source := range sources {
		if source.ProposalConversion {
			count++
		}
	}
	return count
}

func newMCPProposalExtractor(
	config *MCPProposalExtractionConfig,
) (*detective.MCPReadProposalExtractorConfig, error) {
	if config == nil || !config.Enabled {
		return nil, nil
	}
	runner, err := evidenceingestion.NewOllamaExtractorRunner(evidenceingestion.OllamaExtractorConfig{
		BaseURL:      config.BaseURL,
		Model:        config.Model,
		Timeout:      config.Timeout,
		NumPredict:   config.NumPredict,
		PromptMode:   evidenceingestion.OllamaExtractorPromptWholeSpanExactQuote,
		MaxProposals: config.MaxProposals,
	})
	if err != nil {
		return nil, err
	}
	return &detective.MCPReadProposalExtractorConfig{
		MaxProposals:        config.MaxProposals,
		SectionMode:         config.SectionMode,
		MaxSections:         config.MaxSections,
		ExtractorDefinition: runner.ExtractorDefinition(),
		Runner:              runner.Run,
	}, nil
}

func logMCPReadProposalExtraction(
	logger *slog.Logger,
	hostID string,
	sourceBindingID string,
	result detective.MCPReadProposalExtractionResult,
	recovered bool,
) {
	if result.SelectionCoverage != nil {
		logger = logger.With("selection_coverage", result.SelectionCoverage)
	}
	logger.Info(
		"MCP document proposal extraction completed",
		"event", "mcp_read_proposal_extraction_completed",
		"host_id", hostID,
		"source_binding_id", sourceBindingID,
		"source_snapshot_id", result.SourceSnapshotID,
		"extraction_view_id", result.ExtractionViewID,
		"extraction_request_id", result.ExtractionRequestID,
		"extraction_status", result.Status,
		"proposal_count", result.ProposalCount,
		"proposal_occurrence_id", result.ProposalOccurrenceID,
		"model_invoked", result.ModelInvoked,
		"model_call_count", result.ModelCallCount,
		"section_count", result.SectionCount,
		"section_coverage_complete", result.SectionCoverageComplete,
		"replayed", result.Replayed,
		"recovered_at_startup", recovered,
	)
}

func mcpProposalExtractionSourceCount(sources []MCPReadSourceConfig) int {
	count := 0
	for _, source := range sources {
		if source.ProposalExtraction != nil && source.ProposalExtraction.Enabled {
			count++
		}
	}
	return count
}

func logMaintenanceOutcome(logger *slog.Logger, hostID string, outcome maintenanceOutcome, startup bool) {
	logger.Info(
		"repository maintenance completed",
		"event", "repository_maintenance_completed",
		"host_id", hostID,
		"startup", startup,
		"expired_request_id", outcome.expiredRequestID,
		"expired_transition_count", outcome.expiredTransitions,
		"retry_request_id", outcome.retryRequestID,
		"retry_consumption_count", outcome.retryConsumptions,
	)
}

func contextAwareError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
